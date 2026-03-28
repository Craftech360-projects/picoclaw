package livekit

import (
	"context"
	"errors"
	"fmt"
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

	bridgeFactory func() *AgentBridge
	roomFactory   func(job *livekit.Job, assignment *livekit.JobAssignment, bridge *AgentBridge) (*RoomSession, error)

	skipRoomJoin bool
}

// WorkerConfig holds configuration for creating a Worker.
type WorkerConfig struct {
	AgentName     string
	ServerURL     string
	APIKey        string
	APISecret     string
	BridgeFactory func() *AgentBridge
	RoomFactory   func(job *livekit.Job, assignment *livekit.JobAssignment, bridge *AgentBridge) (*RoomSession, error)
}

// NewWorker creates a new LiveKit agent worker.
func NewWorker(cfg WorkerConfig) *Worker {
	return &Worker{
		agentName:     cfg.AgentName,
		serverURL:     cfg.ServerURL,
		apiKey:        cfg.APIKey,
		apiSecret:     cfg.APISecret,
		jobs:          make(map[string]*RoomSession),
		bridgeFactory: cfg.BridgeFactory,
		roomFactory:   cfg.RoomFactory,
	}
}

// Run connects to the LiveKit server and enters the dispatch loop.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("worker is nil")
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
	logger.DebugCF("livekit", "Availability request", map[string]any{
		"job_id": req.Job.Id,
		"room":   jobRoomName(req.Job),
	})
	resp := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:     req.Job.Id,
				Available: true,
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
		bridge = w.bridgeFactory()
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
