package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
)

func heartbeatTestSession(serverURL string) *RoomSession {
	return &RoomSession{
		managerAPIURL:    serverURL,
		managerAPISecret: "test-secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &livekit.Room{Name: "session-1"},
		bridge:           &AgentBridge{},
	}
}

func TestSendUsageHeartbeatPostsCumulativeUsage(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Service-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"cutoff":false}}`))
	}))
	defer server.Close()

	rs := heartbeatTestSession(server.URL)
	cutoff, err := rs.sendUsageHeartbeat(context.Background())
	if err != nil {
		t.Fatalf("sendUsageHeartbeat: %v", err)
	}
	if cutoff {
		t.Fatal("expected no cutoff")
	}
	if gotPath != "/device/aa:bb:cc:dd:ee:ff/usage-heartbeat" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotKey != "test-secret" {
		t.Fatalf("expected X-Service-Key header, got %q", gotKey)
	}
	if gotBody["sessionId"] != "session-1" {
		t.Fatalf("expected sessionId session-1, got %v", gotBody["sessionId"])
	}
	if _, ok := gotBody["sessionDurationSeconds"]; !ok {
		t.Fatal("expected sessionDurationSeconds in payload")
	}
}

func TestSendUsageHeartbeatParsesCutoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"cutoff":true,"reason":"daily_minutes"}}`))
	}))
	defer server.Close()

	cutoff, err := heartbeatTestSession(server.URL).sendUsageHeartbeat(context.Background())
	if err != nil {
		t.Fatalf("sendUsageHeartbeat: %v", err)
	}
	if !cutoff {
		t.Fatal("expected cutoff=true")
	}
}

func TestSendUsageHeartbeatErrorNeverCuts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cutoff, err := heartbeatTestSession(server.URL).sendUsageHeartbeat(context.Background())
	if err == nil {
		t.Fatal("expected an error on 500")
	}
	if cutoff {
		t.Fatal("a failed heartbeat must fail open, not cut")
	}
}

func TestUsageHeartbeatLoopCutoffEndsSessionOnce(t *testing.T) {
	var beats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beats.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"cutoff":true}}`))
	}))
	defer server.Close()

	rs := heartbeatTestSession(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cut := make(chan struct{})
	go rs.usageHeartbeatLoop(ctx, 10*time.Millisecond, func() { close(cut) })

	select {
	case <-cut:
	case <-time.After(2 * time.Second):
		t.Fatal("cutoff callback never fired")
	}
	// The loop must stop after cutoff — no further heartbeats.
	after := beats.Load()
	time.Sleep(50 * time.Millisecond)
	if beats.Load() != after {
		t.Fatalf("loop kept beating after cutoff: %d -> %d", after, beats.Load())
	}
}

func TestUsageHeartbeatLoopSurvivesFailedPosts(t *testing.T) {
	var beats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beats.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	rs := heartbeatTestSession(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go rs.usageHeartbeatLoop(ctx, 10*time.Millisecond, func() { t.Error("must not cut on errors") })

	deadline := time.After(2 * time.Second)
	for beats.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected the loop to keep beating through errors, got %d beats", beats.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
