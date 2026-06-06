package livekit

import (
	"testing"

	lk "github.com/livekit/protocol/livekit"
)

func TestWorkerStatusMessageAvailableBelowCapacity(t *testing.T) {
	w := &Worker{
		jobs:        map[string]*RoomSession{"job-1": nil},
		maxSessions: 4,
	}

	msg := w.workerStatusMessage()
	update := msg.GetUpdateWorker()
	if update == nil {
		t.Fatalf("workerStatusMessage() missing update_worker")
	}
	if got := update.GetStatus(); got != lk.WorkerStatus_WS_AVAILABLE {
		t.Fatalf("status = %v, want %v", got, lk.WorkerStatus_WS_AVAILABLE)
	}
	if got := update.GetJobCount(); got != 1 {
		t.Fatalf("job_count = %d, want 1", got)
	}
	if got := update.GetLoad(); got != 0.25 {
		t.Fatalf("load = %f, want 0.25", got)
	}
}

func TestWorkerStatusMessageFullAtCapacity(t *testing.T) {
	w := &Worker{
		jobs:        map[string]*RoomSession{"job-1": nil, "job-2": nil},
		maxSessions: 2,
	}

	update := w.workerStatusMessage().GetUpdateWorker()
	if got := update.GetStatus(); got != lk.WorkerStatus_WS_FULL {
		t.Fatalf("status = %v, want %v", got, lk.WorkerStatus_WS_FULL)
	}
}

func TestWorkerStatusMessageFullWhenDraining(t *testing.T) {
	w := &Worker{
		jobs:        map[string]*RoomSession{},
		maxSessions: 4,
		draining:    true,
	}

	update := w.workerStatusMessage().GetUpdateWorker()
	if got := update.GetStatus(); got != lk.WorkerStatus_WS_FULL {
		t.Fatalf("status = %v, want %v", got, lk.WorkerStatus_WS_FULL)
	}
}
