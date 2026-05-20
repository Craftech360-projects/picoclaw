package livekit

import (
	"strings"
	"testing"
)

func TestWorkerPrometheusMetricsIncludesSessionLoadPercent(t *testing.T) {
	w := &Worker{
		agentName:   "cheeko-agent",
		jobs:        map[string]*RoomSession{"a": nil, "b": nil, "c": nil},
		maxSessions: 12,
		connected:   true,
		ready:       true,
	}

	got := w.prometheusMetrics()

	if !strings.Contains(got, "picoclaw_livekit_active_sessions{agent_name=\"cheeko-agent\"} 3") {
		t.Fatalf("active sessions metric missing or invalid: %s", got)
	}
	if !strings.Contains(got, "picoclaw_livekit_max_sessions{agent_name=\"cheeko-agent\"} 12") {
		t.Fatalf("max sessions metric missing or invalid: %s", got)
	}
	if !strings.Contains(got, "picoclaw_livekit_session_load_percent{agent_name=\"cheeko-agent\"} 25.000000") {
		t.Fatalf("session load percent metric missing or invalid: %s", got)
	}
}

func TestWorkerPrometheusMetricsEscapesAgentLabel(t *testing.T) {
	w := &Worker{
		agentName:   `agent"name\one`,
		jobs:        map[string]*RoomSession{},
		maxSessions: 10,
	}

	got := w.prometheusMetrics()

	if !strings.Contains(got, `agent_name="agent\"name\\one"`) {
		t.Fatalf("agent label escaping mismatch: %s", got)
	}
}

