package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	w.conn = conn
	w.mu.Lock()
	w.connected = true
	w.mu.Unlock()
	logger.InfoCF("livekit", "Connected to LiveKit agent endpoint", map[string]any{
		"ws_url": wsURL,
	})

	if err := w.sendRegister(); err != nil {
		_ = conn.Close()
		return err
	}

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go w.pingLoop(pingCtx)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
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
			w.workerID = m.Register.WorkerId
			w.mu.Lock()
			w.ready = true
			w.mu.Unlock()
			logger.InfoCF("livekit", "Worker registered", map[string]any{
				"worker_id": w.workerID,
				"agent":     w.agentName,
			})
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
	w.mu.RUnlock()

	available := currentJobs < w.maxSessions

	logger.DebugCF("livekit", "Availability request", map[string]any{
		"job_id":       req.Job.Id,
		"room":         jobRoomName(req.Job),
		"current_jobs": currentJobs,
		"max_sessions": w.maxSessions,
		"available":    available,
	})

	resp := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:     req.Job.Id,
				Available: available,
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
	logger.InfoCF("livekit", "Job assignment received", map[string]any{
		"job_id": job.Id,
		"room":   jobRoomName(job),
	})

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
	if w.conn == nil {
		return fmt.Errorf("not connected")
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
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

	if w.conn != nil {
		_ = w.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = w.conn.Close()
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
		w.mu.RUnlock()

		if !connected || !ready {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(rw).Encode(map[string]any{
				"status": "not ready",
				"connected": connected,
				"registered": ready,
			})
			return
		}

		// Worker is ready if it has capacity for more sessions
		available := activeJobs < maxSessions
		statusCode := http.StatusOK
		status := "ready"
		if !available {
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
		})
	})

	w.healthServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", w.healthPort),
		Handler: mux,
	}

	go func() {
		logger.InfoCF("livekit", "Health check server started", map[string]any{
			"port": w.healthPort,
			"health": fmt.Sprintf("http://0.0.0.0:%d/health", w.healthPort),
			"ready": fmt.Sprintf("http://0.0.0.0:%d/ready", w.healthPort),
		})
		if err := w.healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("livekit", "Health check server error", map[string]any{
				"error": err.Error(),
			})
		}
	}()
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
