package livekit

import (
	"testing"
	"time"

	lk "github.com/livekit/protocol/livekit"
)

func TestWorkerChaosDuplicateDispatchDoesNotStartSecondSession(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})
	w.jobs["job-1"] = &RoomSession{roomInfo: &lk.Room{Name: "room-a"}}

	factoryCalls := 0
	w.roomFactory = func(job *lk.Job, assignment *lk.JobAssignment, bridge *AgentBridge) (*RoomSession, error) {
		factoryCalls++
		return &RoomSession{roomInfo: &lk.Room{Name: "room-a"}}, nil
	}

	w.handleAssignment(t.Context(), &lk.JobAssignment{
		Job: &lk.Job{
			Id:   "job-1",
			Room: &lk.Room{Name: "room-a"},
		},
	})

	if factoryCalls != 0 {
		t.Fatalf("roomFactory calls = %d, want 0 for duplicate dispatch", factoryCalls)
	}
	if got := len(w.jobs); got != 1 {
		t.Fatalf("active jobs = %d, want existing job only", got)
	}
}

func TestWorkerChaosRollingDeployDrainCompletesAfterActiveJobCleanup(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})
	w.jobs["job-1"] = &RoomSession{roomInfo: &lk.Room{Name: "room-a"}}
	w.BeginDraining()

	done := make(chan error, 1)
	go func() {
		done <- w.WaitForDrain(500 * time.Millisecond)
	}()

	time.Sleep(25 * time.Millisecond)
	w.removeJob("job-1", nil)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForDrain() error = %v, want nil after job cleanup", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForDrain did not return after active job cleanup")
	}
}

func TestWorkerLoadAtTwelveConcurrentSessionsReportsFullCapacity(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})
	for i := 0; i < 12; i++ {
		w.jobs[string(rune('a'+i))] = &RoomSession{}
	}

	update := w.workerStatusMessage().GetUpdateWorker()
	if update == nil {
		t.Fatal("workerStatusMessage() missing update_worker")
	}
	if got := update.GetJobCount(); got != 12 {
		t.Fatalf("job_count = %d, want 12", got)
	}
	if got := update.GetStatus(); got != lk.WorkerStatus_WS_FULL {
		t.Fatalf("status = %v, want %v at 12/12 load", got, lk.WorkerStatus_WS_FULL)
	}
	if got := update.GetLoad(); got != 1 {
		t.Fatalf("load = %f, want 1.0 at 12/12 load", got)
	}
}
