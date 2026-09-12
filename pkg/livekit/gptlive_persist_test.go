package livekit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	protocol "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/gptlive"
)

// TestPersistGPTLiveSessionSendsUsageAndTranscript is the test Task 13's
// review (Important 4) says should have existed from the start: it is what
// would have caught Critical 1 (persistGPTLiveSession populating only
// TotalTokens, leaving InputTokens/OutputTokens at zero, which made
// sendUsageSummary's own guard — "if usage.InputTokens == 0 &&
// usage.OutputTokens == 0 { return nil }" — skip the POST to
// /device/token-usage for every single gptlive session, silently). This
// drives a real pipeline through onEvent exactly the way a live session would
// (a user turn, an agent turn, a BackendUsage report, a VoiceUsage report),
// then calls persistGPTLiveSession against a fake manager API and asserts all
// three endpoints were actually hit with the expected payload contents.
func TestPersistGPTLiveSessionSendsUsageAndTranscript(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hello", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "hi there", Text: "hi there"})
	p.onBurstClose()
	p.onEvent(gptlive.BackendUsage{Total: 120, Input: 80, Output: 40})
	p.onEvent(gptlive.VoiceUsage{Seconds: 12.5})

	var chatHistoryHit, sessionEndHit, usageHit bool
	var chatHistoryPayload, sessionEndPayload, usagePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat-history/session":
			chatHistoryHit = true
			if err := json.NewDecoder(r.Body).Decode(&chatHistoryPayload); err != nil {
				t.Fatalf("decode chat-history payload: %v", err)
			}
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/end":
			sessionEndHit = true
			if err := json.NewDecoder(r.Body).Decode(&sessionEndPayload); err != nil {
				t.Fatalf("decode session-end payload: %v", err)
			}
		case "/device/token-usage":
			usageHit = true
			if err := json.NewDecoder(r.Body).Decode(&usagePayload); err != nil {
				t.Fatalf("decode usage payload: %v", err)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	rs.persistGPTLiveSession(p)

	if !chatHistoryHit {
		t.Error("chat-history endpoint was never hit")
	}
	if !sessionEndHit {
		t.Error("session-end endpoint was never hit")
	}
	if !usageHit {
		t.Fatal("usage endpoint was never hit — this is exactly Critical 1: InputTokens/OutputTokens must be non-zero for sendUsageSummary to actually POST")
	}
	if got := chatHistoryPayload["messageCount"]; got != float64(2) {
		t.Errorf("chat-history messageCount = %#v, want 2", got)
	}
	if got := sessionEndPayload["messageCount"]; got != float64(2) {
		t.Errorf("session-end messageCount = %#v, want 2", got)
	}
	if got := usagePayload["inputTokens"]; got != float64(80) {
		t.Errorf("usage inputTokens = %#v, want 80", got)
	}
	if got := usagePayload["outputTokens"]; got != float64(40) {
		t.Errorf("usage outputTokens = %#v, want 40", got)
	}
	if got := usagePayload["totalTokens"]; got != float64(120) {
		t.Errorf("usage totalTokens = %#v, want 120", got)
	}
	if got := usagePayload["sessionDurationSeconds"]; got != float64(12.5) {
		t.Errorf("usage sessionDurationSeconds = %#v, want 12.5", got)
	}
}

// TestPersistGPTLiveSessionWithNoUsageSkipsUsagePost covers the mirror case:
// a pipeline that never produced a transcript or any backend/voice usage (a
// session that connected and was torn down before any turn completed) must
// not post an empty chat-history payload, still reports the session end (so
// the manager API's session record closes out), and correctly skips the
// usage endpoint — zero tokens really is "nothing to report" here, not a
// dropped update, since sendChatHistory/sendSessionEnd/sendUsageSummary's own
// guards are what decide this, not persistGPTLiveSession.
func TestPersistGPTLiveSessionWithNoUsageSkipsUsagePost(t *testing.T) {
	p := &gptLivePipeline{}

	var chatHistoryHit, sessionEndHit, usageHit bool
	var sessionEndPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat-history/session":
			chatHistoryHit = true
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/end":
			sessionEndHit = true
			if err := json.NewDecoder(r.Body).Decode(&sessionEndPayload); err != nil {
				t.Fatalf("decode session-end payload: %v", err)
			}
		case "/device/token-usage":
			usageHit = true
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	rs.persistGPTLiveSession(p)

	if chatHistoryHit {
		t.Error("chat-history endpoint was hit for an empty transcript; sendChatHistory should have no-op'd")
	}
	if !sessionEndHit {
		t.Error("session-end endpoint was never hit; it should still close out the session record even with no usage")
	}
	if got := sessionEndPayload["messageCount"]; got != float64(0) {
		t.Errorf("session-end messageCount = %#v, want 0", got)
	}
	if usageHit {
		t.Error("usage endpoint was hit for a session with zero tokens; sendUsageSummary's own guard should have skipped it")
	}
}
