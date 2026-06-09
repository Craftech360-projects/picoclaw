package livekit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	lk "github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

func TestWorkerRemovesJobAndLeavesSessionWhenJoinFails(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 1,
	})
	session := &RoomSession{
		roomInfo:  &lk.Room{Name: "room-a"},
		serverURL: "ws://localhost:7880",
	}
	w.roomFactory = func(job *lk.Job, assignment *lk.JobAssignment, bridge *AgentBridge) (*RoomSession, error) {
		return session, nil
	}

	w.handleAssignment(context.Background(), &lk.JobAssignment{
		Job: &lk.Job{
			Id:   "job-1",
			Room: &lk.Room{Name: "room-a"},
		},
	})

	waitForWorkerCondition(t, func() bool {
		w.mu.RLock()
		jobsCleared := len(w.jobs) == 0
		w.mu.RUnlock()
		if !jobsCleared {
			return false
		}
		session.mu.Lock()
		sessionLeft := session.cancel == nil
		session.mu.Unlock()
		return sessionLeft
	})
}

func TestWorkerRunOnceResetsConnectedAndReadyAfterWebsocketLoss(t *testing.T) {
	registerSent := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err != nil {
			t.Errorf("Upgrade error = %v", err)
			return
		}
		defer conn.Close()
		register := &lk.ServerMessage{
			Message: &lk.ServerMessage_Register{
				Register: &lk.RegisterWorkerResponse{WorkerId: "worker-1"},
			},
		}
		data, err := proto.Marshal(register)
		if err != nil {
			t.Errorf("Marshal register error = %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			t.Errorf("WriteMessage error = %v", err)
			return
		}
		close(registerSent)
		time.Sleep(20 * time.Millisecond)
	}))
	defer server.Close()

	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   server.URL,
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 1,
	})

	err := w.runOnce(context.Background())
	if err == nil {
		t.Fatal("runOnce() expected websocket close error")
	}
	<-registerSent

	w.mu.RLock()
	connected := w.connected
	ready := w.ready
	w.mu.RUnlock()
	if connected || ready {
		t.Fatalf("connected=%v ready=%v, want both false after websocket loss", connected, ready)
	}
}

func waitForWorkerCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
