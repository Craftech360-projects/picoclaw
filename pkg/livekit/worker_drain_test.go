package livekit

import (
	"strings"
	"testing"
	"time"
)

func TestWorkerBeginDrainingSetsFlag(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})

	if w.draining {
		t.Fatalf("draining should be false before BeginDraining")
	}
	w.BeginDraining()
	if !w.draining {
		t.Fatalf("draining should be true after BeginDraining")
	}
}

func TestWorkerWaitForDrainImmediateWhenNoJobs(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})

	if err := w.WaitForDrain(50 * time.Millisecond); err != nil {
		t.Fatalf("WaitForDrain() unexpected error: %v", err)
	}
}

func TestWorkerWaitForDrainTimesOutWithActiveJobs(t *testing.T) {
	w := NewWorker(WorkerConfig{
		AgentName:   "test-agent",
		ServerURL:   "ws://localhost:7880",
		APIKey:      "key",
		APISecret:   "secret",
		MaxSessions: 12,
	})
	w.jobs["job-1"] = nil

	err := w.WaitForDrain(50 * time.Millisecond)
	if err == nil {
		t.Fatalf("WaitForDrain() expected timeout error")
	}
	if !strings.Contains(err.Error(), "drain timeout") {
		t.Fatalf("WaitForDrain() error = %q, want drain timeout", err.Error())
	}
}
