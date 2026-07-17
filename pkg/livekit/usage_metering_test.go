package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestRecordUsageWarnsOnceWhenUsageMissing(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "usage.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	t.Cleanup(logger.DisableFileLogging)

	ab := &AgentBridge{modelID: "openai/gpt-4.1-mini"}
	ab.recordUsage(nil, 250*time.Millisecond)
	ab.recordUsage(nil, 250*time.Millisecond)
	logger.DisableFileLogging()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logs := string(data)
	if !strings.Contains(logs, `"level":"warn"`) {
		t.Fatalf("expected WARN log when usage is nil, got:\n%s", logs)
	}
	if !strings.Contains(logs, "openai/gpt-4.1-mini") {
		t.Fatalf("WARN log should name the model, got:\n%s", logs)
	}
	if n := strings.Count(logs, "carried no usage"); n != 1 {
		t.Fatalf("WARN should fire once per session, fired %d times:\n%s", n, logs)
	}
}

func TestSilentSessionPersistsDurationAndNothingElse(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var usagePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/device/token-usage" {
			if err := json.NewDecoder(r.Body).Decode(&usagePayload); err != nil {
				t.Errorf("decode usage payload: %v", err)
			}
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	// A child who connects and never speaks: no tokens, no transcript,
	// but the session lasted — trial days and minute caps must still count it.
	bridge := &AgentBridge{
		provider: fakeSummaryProvider{},
		sessions: session.NewSessionManager(""),
	}
	bridge.usage.sessionStart = time.Now().Add(-42 * time.Second)

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	rs.persistPostSessionData(bridge)

	if usagePayload == nil {
		t.Fatal("expected a usage POST for a zero-token session with duration > 0")
	}
	if got := usagePayload["totalTokens"]; got != float64(0) {
		t.Fatalf("totalTokens = %#v, want 0", got)
	}
	duration, _ := usagePayload["sessionDurationSeconds"].(float64)
	if duration <= 0 {
		t.Fatalf("sessionDurationSeconds = %#v, want > 0", usagePayload["sessionDurationSeconds"])
	}
	// No summary PUT, chat history, session end, or summarize LLM call —
	// reconnect churn must not burn tokens or duplicate summaries.
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p != "/device/token-usage" {
			t.Fatalf("silent session hit %s; only the usage POST is allowed", p)
		}
	}
}

func TestSummarizeBatchRecordsTokensWithoutCountingATurn(t *testing.T) {
	ab := &AgentBridge{
		provider: fakeSummaryProvider{usage: &providers.UsageInfo{PromptTokens: 123, CompletionTokens: 45}},
		modelID:  "test-model",
	}

	_, err := ab.bridgeSummarizeBatch(
		context.Background(),
		[]providers.Message{{Role: "user", Content: "hello"}},
		"",
	)
	if err != nil {
		t.Fatalf("bridgeSummarizeBatch() error = %v", err)
	}

	snap := ab.UsageSnapshot()
	if snap.InputTokens != 123 || snap.OutputTokens != 45 {
		t.Fatalf("usage after summarize = input %d / output %d, want 123 / 45",
			snap.InputTokens, snap.OutputTokens)
	}
	if snap.MessageCount != 0 {
		t.Fatalf("MessageCount = %d after summarize; internal calls must not count as turns", snap.MessageCount)
	}
}

func TestFinalizeSummaryTokensAreBilled(t *testing.T) {
	var usagePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device/token-usage" {
			if err := json.NewDecoder(r.Body).Decode(&usagePayload); err != nil {
				t.Errorf("decode usage payload: %v", err)
			}
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	sessionKey := "livekit:device:aabbccddeeff"
	sessions := session.NewSessionManager("")
	sessions.AddMessage(sessionKey, "user", "hello")
	sessions.AddMessage(sessionKey, "assistant", "hi")

	bridge := &AgentBridge{
		provider: fakeSummaryProvider{usage: &providers.UsageInfo{PromptTokens: 200, CompletionTokens: 30}},
		sessions: sessions,
		historyLog: []PersistedChatMessage{
			{ChatType: chatTypeUser, Content: "hello", Timestamp: 1},
			{ChatType: chatTypeAssistant, Content: "hi", Timestamp: 2},
		},
	}
	bridge.usage.sessionStart = time.Now().Add(-10 * time.Second)

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	rs.persistPostSessionData(bridge)

	if usagePayload == nil {
		t.Fatal("expected a usage POST")
	}
	// The end-of-session summarize call spends 230 tokens; the POSTed row must
	// include them — usage is snapshotted after finalize, not before.
	if got := usagePayload["totalTokens"]; got != float64(230) {
		t.Fatalf("totalTokens = %#v, want 230 (finalize-summary tokens billed)", got)
	}
}
