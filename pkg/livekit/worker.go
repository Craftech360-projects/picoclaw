package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/logger"
	"google.golang.org/protobuf/proto"
)

// Worker implements the LiveKit agent worker dispatch protocol.
type Worker struct {
	agentName string
	serverURL string
	apiKey    string
	apiSecret string

	conn     *websocket.Conn
	workerID string
	jobs     map[string]*RoomSession
	mu       sync.RWMutex
	sendMu   sync.Mutex

	bridgeFactory func(job *livekit.Job) *AgentBridge
	roomFactory   func(job *livekit.Job, assignment *livekit.JobAssignment, bridge *AgentBridge) (*RoomSession, error)

	skipRoomJoin bool
	maxSessions  int // Maximum number of concurrent sessions this worker will handle

	// Health check server
	healthPort   int
	healthServer *http.Server
	connected    bool // true after successful WebSocket connection
	ready        bool // true after worker registration
	draining     bool // true when SIGTERM/SIGINT drain mode begins
}

// WorkerConfig holds configuration for creating a Worker.
type WorkerConfig struct {
	AgentName     string
	ServerURL     string
	APIKey        string
	APISecret     string
	BridgeFactory func(job *livekit.Job) *AgentBridge
	RoomFactory   func(job *livekit.Job, assignment *livekit.JobAssignment, bridge *AgentBridge) (*RoomSession, error)
	MaxSessions   int
	HealthPort    int // HTTP port for /health and /ready endpoints (0 = disabled)
}

// NewWorker creates a new LiveKit agent worker.
func NewWorker(cfg WorkerConfig) *Worker {
	maxSessions := cfg.MaxSessions
	if maxSessions <= 0 {
		maxSessions = 100 // Default sensible limit to prevent OOM
	}
	return &Worker{
		agentName:     cfg.AgentName,
		serverURL:     cfg.ServerURL,
		apiKey:        cfg.APIKey,
		apiSecret:     cfg.APISecret,
		jobs:          make(map[string]*RoomSession),
		bridgeFactory: cfg.BridgeFactory,
		roomFactory:   cfg.RoomFactory,
		maxSessions:   maxSessions,
		healthPort:    cfg.HealthPort,
	}
}

// Run connects to the LiveKit server and enters the dispatch loop.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("worker is nil")
	}

	// Start health check server if port is configured
	if w.healthPort > 0 {
		w.startHealthServer()
	}

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := w.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.WarnCF("livekit", "Worker disconnected; retrying", map[string]any{
				"error":   err.Error(),
				"backoff": backoff.String(),
			})
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		return nil
	}
}

func (w *Worker) runOnce(ctx context.Context) error {
	wsURL, err := agentWebsocketURL(w.serverURL)
	if err != nil {
		return err
	}
	token, err := w.workerToken()
	if err != nil {
		return err
	}

	headers := map[string][]string{
		"Authorization": {"Bearer " + token},
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}
	w.markConnected(conn)
	defer w.markDisconnected(conn)
	logger.InfoCF("livekit", "Connected to LiveKit agent endpoint", map[string]any{
		"ws_url": wsURL,
	})

	if err := w.sendRegister(); err != nil {
		return err
	}

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go w.pingLoop(pingCtx)
	go w.workerStatusLoop(pingCtx)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		msg := &livekit.ServerMessage{}
		if err := proto.Unmarshal(data, msg); err != nil {
			continue
		}
		w.handleServerMessage(ctx, msg)
	}
}

func (w *Worker) handleServerMessage(ctx context.Context, msg *livekit.ServerMessage) {
	switch m := msg.Message.(type) {
	case *livekit.ServerMessage_Register:
		if m.Register != nil {
			w.mu.Lock()
			w.workerID = m.Register.WorkerId
			w.ready = true
			w.mu.Unlock()
			logger.InfoCF("livekit", "Worker registered", map[string]any{
				"worker_id": w.workerID,
				"agent":     w.agentName,
			})
			w.sendWorkerStatus()
		}
	case *livekit.ServerMessage_Availability:
		w.handleAvailability(m.Availability)
	case *livekit.ServerMessage_Assignment:
		w.handleAssignment(ctx, m.Assignment)
	case *livekit.ServerMessage_Termination:
		w.handleTermination(m.Termination)
	case *livekit.ServerMessage_Pong:
		return
	default:
		return
	}
}

func (w *Worker) handleAvailability(req *livekit.AvailabilityRequest) {
	if req == nil || req.Job == nil {
		return
	}

	w.mu.RLock()
	currentJobs := len(w.jobs)
	draining := w.draining
	w.mu.RUnlock()

	available := currentJobs < w.maxSessions
	if draining {
		available = false
	}

	logger.DebugCF("livekit", "Availability request", map[string]any{
		"job_id":       req.Job.Id,
		"room":         jobRoomName(req.Job),
		"current_jobs": currentJobs,
		"max_sessions": w.maxSessions,
		"available":    available,
		"draining":     draining,
	})

	resp := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:     req.Job.Id,
				Available: available,
				ParticipantAttributes: map[string]string{
					"lk.agent.name": w.agentName,
				},
			},
		},
	}
	_ = w.sendProto(resp)
}

func (w *Worker) handleAssignment(ctx context.Context, assignment *livekit.JobAssignment) {
	if assignment == nil || assignment.Job == nil {
		return
	}
	job := assignment.Job
	roomMetadataBytes := 0
	if job.Room != nil {
		roomMetadataBytes = len(strings.TrimSpace(job.Room.Metadata))
	}
	jobMetadataBytes := len(strings.TrimSpace(job.Metadata))
	logger.InfoCF("livekit", "Job assignment received", map[string]any{
		"job_id":              job.Id,
		"room":                jobRoomName(job),
		"dispatch_id":         job.DispatchId,
		"agent_name":          job.AgentName,
		"room_metadata_bytes": roomMetadataBytes,
		"job_metadata_bytes":  jobMetadataBytes,
	})

	if w.hasJob(job.Id) {
		logger.WarnCF("livekit", "Duplicate job assignment ignored", map[string]any{
			"job_id": job.Id,
			"room":   jobRoomName(job),
		})
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_RUNNING)
		return
	}

	if w.skipRoomJoin {
		w.mu.Lock()
		w.jobs[job.Id] = nil
		w.mu.Unlock()
		return
	}

	var bridge *AgentBridge
	if w.bridgeFactory != nil {
		bridge = w.bridgeFactory(job)
	}
	if w.roomFactory == nil {
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED)
		return
	}

	session, err := w.roomFactory(job, assignment, bridge)
	if err != nil {
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED)
		return
	}

	w.mu.Lock()
	w.jobs[job.Id] = session
	w.mu.Unlock()

	go func() {
		if err := session.Join(ctx); err != nil {
			w.removeJob(job.Id, session)
			session.Leave()
			logger.ErrorCF("livekit", "Room join failed", map[string]any{
				"job_id": job.Id,
				"room":   jobRoomName(job),
				"error":  err.Error(),
			})
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED)
			return
		}
		logger.InfoCF("livekit", "Room session running", map[string]any{
			"job_id": job.Id,
			"room":   jobRoomName(job),
		})
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_RUNNING)
	}()
}

func (w *Worker) hasJob(jobID string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.jobs[jobID]
	return ok
}

func (w *Worker) removeJob(jobID string, session *RoomSession) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	current, ok := w.jobs[jobID]
	if !ok {
		return false
	}
	if session != nil && current != session {
		return false
	}
	delete(w.jobs, jobID)
	return true
}

func (w *Worker) handleTermination(term *livekit.JobTermination) {
	if term == nil {
		return
	}
	logger.InfoCF("livekit", "Job termination", map[string]any{
		"job_id": term.JobId,
	})
	w.mu.Lock()
	session, ok := w.jobs[term.JobId]
	delete(w.jobs, term.JobId)
	w.mu.Unlock()

	if ok && session != nil {
		session.Leave()
	}
}

func (w *Worker) updateJobStatus(jobID string, status livekit.JobStatus) {
	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateJob{
			UpdateJob: &livekit.UpdateJobStatus{
				JobId:  jobID,
				Status: status,
			},
		},
	}
	_ = w.sendProto(msg)
}

func (w *Worker) sendRegister() error {
	if w.agentName == "" {
		return errors.New("agent name is required")
	}
	logger.InfoCF("livekit", "Registering worker", map[string]any{
		"agent": w.agentName,
	})
	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: &livekit.RegisterWorkerRequest{
				Type:         livekit.JobType_JT_ROOM,
				AgentName:    w.agentName,
				Version:      "picoclaw-livekit",
				PingInterval: 10,
				AllowedPermissions: &livekit.ParticipantPermission{
					CanPublish:        true,
					CanSubscribe:      true,
					CanPublishData:    true,
					CanUpdateMetadata: true,
					Agent:             true,
				},
			},
		},
	}
	return w.sendProto(msg)
}

func (w *Worker) sendProto(msg *livekit.WorkerMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	w.mu.RLock()
	conn := w.conn
	w.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

func (w *Worker) markConnected(conn *websocket.Conn) {
	w.mu.Lock()
	w.conn = conn
	w.connected = true
	w.ready = false
	w.mu.Unlock()
}

func (w *Worker) markDisconnected(conn *websocket.Conn) {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	w.mu.Lock()
	if conn == nil || w.conn == conn {
		w.conn = nil
		w.connected = false
		w.ready = false
	}
	w.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (w *Worker) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg := &livekit.WorkerMessage{
				Message: &livekit.WorkerMessage_Ping{
					Ping: &livekit.WorkerPing{Timestamp: time.Now().Unix()},
				},
			}
			if err := w.sendProto(msg); err != nil {
				return
			}
		}
	}
}

func (w *Worker) workerStatusLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sendWorkerStatus()
		}
	}
}

func (w *Worker) workerStatusMessage() *livekit.WorkerMessage {
	w.mu.RLock()
	activeJobs := len(w.jobs)
	maxSessions := w.maxSessions
	draining := w.draining
	w.mu.RUnlock()

	status := livekit.WorkerStatus_WS_AVAILABLE
	if draining || (maxSessions > 0 && activeJobs >= maxSessions) {
		status = livekit.WorkerStatus_WS_FULL
	}

	load := float32(0)
	if maxSessions > 0 {
		load = float32(activeJobs) / float32(maxSessions)
	}

	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateWorker{
			UpdateWorker: &livekit.UpdateWorkerStatus{
				Load:     load,
				Status:   &status,
				JobCount: uint32(activeJobs),
			},
		},
	}
}

func (w *Worker) sendWorkerStatus() {
	if err := w.sendProto(w.workerStatusMessage()); err != nil {
		logger.WarnCF("livekit", "Worker status update failed", map[string]any{
			"error": err.Error(),
		})
	}
}

// Shutdown gracefully stops all jobs and disconnects.
func (w *Worker) Shutdown() {
	// Stop health check server
	if w.healthServer != nil {
		_ = w.healthServer.Shutdown(context.Background())
	}

	w.mu.RLock()
	sessions := make([]*RoomSession, 0, len(w.jobs))
	for _, s := range w.jobs {
		if s != nil {
			sessions = append(sessions, s)
		}
	}
	w.mu.RUnlock()

	for _, s := range sessions {
		s.Leave()
	}

	w.mu.RLock()
	conn := w.conn
	w.mu.RUnlock()
	if conn != nil {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
	w.markDisconnected(conn)
}

// BeginDraining stops the worker from accepting new jobs while allowing active jobs to finish.
func (w *Worker) BeginDraining() {
	w.mu.Lock()
	w.draining = true
	activeJobs := len(w.jobs)
	w.mu.Unlock()
	logger.InfoCF("livekit", "Worker entering drain mode", map[string]any{
		"active_jobs": activeJobs,
	})
}

// WaitForDrain waits for active jobs to complete until timeout expires.
func (w *Worker) WaitForDrain(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		w.mu.RLock()
		activeJobs := len(w.jobs)
		w.mu.RUnlock()
		if activeJobs == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("drain timeout with %d active jobs", activeJobs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (w *Worker) workerToken() (string, error) {
	if w.apiKey == "" || w.apiSecret == "" {
		return "", errors.New("livekit api key/secret missing")
	}
	at := auth.NewAccessToken(w.apiKey, w.apiSecret)
	grant := &auth.VideoGrant{Agent: true}
	at.SetVideoGrant(grant)
	at.SetIdentity("picoclaw-worker")
	return at.ToJWT()
}

// startHealthServer starts an HTTP server with /health and /ready endpoints.
func (w *Worker) startHealthServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.RLock()
		connected := w.connected
		activeJobs := len(w.jobs)
		w.mu.RUnlock()

		if !connected {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(rw).Encode(map[string]any{
				"status": "unhealthy",
				"reason": "not connected to LiveKit",
			})
			return
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		json.NewEncoder(rw).Encode(map[string]any{
			"status":      "healthy",
			"activeJobs":  activeJobs,
			"maxSessions": w.maxSessions,
		})
	})

	mux.HandleFunc("/ready", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.RLock()
		connected := w.connected
		ready := w.ready
		activeJobs := len(w.jobs)
		maxSessions := w.maxSessions
		draining := w.draining
		w.mu.RUnlock()

		if !connected || !ready {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(rw).Encode(map[string]any{
				"status":     "not ready",
				"connected":  connected,
				"registered": ready,
			})
			return
		}

		// Worker is ready if it has capacity for more sessions
		available := activeJobs < maxSessions
		if draining {
			available = false
		}
		statusCode := http.StatusOK
		status := "ready"
		if draining {
			statusCode = http.StatusServiceUnavailable
			status = "draining"
		} else if !available {
			statusCode = http.StatusTooManyRequests
			status = "at capacity"
		}

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(statusCode)
		json.NewEncoder(rw).Encode(map[string]any{
			"status":      status,
			"activeJobs":  activeJobs,
			"maxSessions": maxSessions,
			"available":   available,
			"draining":    draining,
		})
	})

	mux.HandleFunc("/metrics", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = rw.Write([]byte(w.prometheusMetrics()))
	})

	w.healthServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", w.healthPort),
		Handler: mux,
	}

	go func() {
		logger.InfoCF("livekit", "Health check server started", map[string]any{
			"port":   w.healthPort,
			"health": fmt.Sprintf("http://0.0.0.0:%d/health", w.healthPort),
			"ready":  fmt.Sprintf("http://0.0.0.0:%d/ready", w.healthPort),
		})
		if err := w.healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("livekit", "Health check server error", map[string]any{
				"error": err.Error(),
			})
		}
	}()
}

func (w *Worker) prometheusMetrics() string {
	w.mu.RLock()
	activeJobs := len(w.jobs)
	maxSessions := w.maxSessions
	draining := w.draining
	ready := w.ready
	connected := w.connected
	agentName := w.agentName
	w.mu.RUnlock()

	loadPercent := 0.0
	if maxSessions > 0 {
		loadPercent = (float64(activeJobs) / float64(maxSessions)) * 100.0
	}
	drainingValue := 0
	if draining {
		drainingValue = 1
	}
	readyValue := 0
	if ready {
		readyValue = 1
	}
	connectedValue := 0
	if connected {
		connectedValue = 1
	}

	escapedAgent := strings.ReplaceAll(strings.ReplaceAll(agentName, "\\", "\\\\"), "\"", "\\\"")
	var sb strings.Builder
	sb.WriteString("# HELP picoclaw_livekit_active_sessions Active LiveKit sessions handled by this worker.\n")
	sb.WriteString("# TYPE picoclaw_livekit_active_sessions gauge\n")
	sb.WriteString("picoclaw_livekit_active_sessions{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.Itoa(activeJobs))
	sb.WriteString("\n")

	sb.WriteString("# HELP picoclaw_livekit_max_sessions Configured max concurrent LiveKit sessions for this worker.\n")
	sb.WriteString("# TYPE picoclaw_livekit_max_sessions gauge\n")
	sb.WriteString("picoclaw_livekit_max_sessions{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.Itoa(maxSessions))
	sb.WriteString("\n")

	sb.WriteString("# HELP picoclaw_livekit_session_load_percent Session load as percentage of max sessions.\n")
	sb.WriteString("# TYPE picoclaw_livekit_session_load_percent gauge\n")
	sb.WriteString("picoclaw_livekit_session_load_percent{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.FormatFloat(loadPercent, 'f', 6, 64))
	sb.WriteString("\n")

	sb.WriteString("# HELP picoclaw_livekit_draining Whether this worker is draining (1) or not (0).\n")
	sb.WriteString("# TYPE picoclaw_livekit_draining gauge\n")
	sb.WriteString("picoclaw_livekit_draining{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.Itoa(drainingValue))
	sb.WriteString("\n")

	sb.WriteString("# HELP picoclaw_livekit_ready Whether this worker is registered and ready (1) or not (0).\n")
	sb.WriteString("# TYPE picoclaw_livekit_ready gauge\n")
	sb.WriteString("picoclaw_livekit_ready{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.Itoa(readyValue))
	sb.WriteString("\n")

	sb.WriteString("# HELP picoclaw_livekit_connected Whether this worker websocket is connected (1) or not (0).\n")
	sb.WriteString("# TYPE picoclaw_livekit_connected gauge\n")
	sb.WriteString("picoclaw_livekit_connected{agent_name=\"")
	sb.WriteString(escapedAgent)
	sb.WriteString("\"} ")
	sb.WriteString(strconv.Itoa(connectedValue))
	sb.WriteString("\n")

	return sb.String()
}

func agentWebsocketURL(serverURL string) (string, error) {
	if serverURL == "" {
		return "", errors.New("server url is empty")
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// keep
	default:
		if u.Scheme == "" {
			u.Scheme = "wss"
		} else {
			return "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
		}
	}

	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/agent"
	} else if !strings.HasSuffix(path, "/agent") {
		path = path + "/agent"
	}
	u.Path = path

	return u.String(), nil
}

func jobRoomName(job *livekit.Job) string {
	if job == nil || job.Room == nil {
		return ""
	}
	return job.Room.Name
}
