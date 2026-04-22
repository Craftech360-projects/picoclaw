package session_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestManagerAPIBackendHydratesHistoryFromBootstrap(t *testing.T) {
	var gotServiceKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/device/AA:BB:CC:DD:EE:FF/bootstrap" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotServiceKey = r.Header.Get("X-Service-Key")
		if r.URL.Query().Get("includeMemories") != "false" {
			t.Fatalf("includeMemories query = %q", r.URL.Query().Get("includeMemories"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"agent": map[string]any{
					"summaryMemory": "Asha likes space.",
				},
				"recentMessages": []map[string]any{
					{"role": "user", "content": "Hello"},
					{"role": "assistant", "content": "Hi Asha!"},
				},
			},
		})
	}))
	defer server.Close()

	store := session.NewManagerAPIBackend(session.ManagerAPIBackendConfig{
		BaseURL:    server.URL,
		ServiceKey: "service-secret",
		MACAddress: "AA:BB:CC:DD:EE:FF",
		AgentID:    "agent-id",
		SessionID:  "room-1",
	})

	history := store.GetHistory("livekit:device:AABBCCDDEEFF")
	if gotServiceKey != "service-secret" {
		t.Fatalf("X-Service-Key = %q, want service-secret", gotServiceKey)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "Hello" {
		t.Fatalf("unexpected first history item: %+v", history[0])
	}
	if got := store.GetSummary("livekit:device:AABBCCDDEEFF"); got != "Asha likes space." {
		t.Fatalf("summary = %q", got)
	}
}

func TestManagerAPIBackendReportsUserAndAssistantMessages(t *testing.T) {
	var reportPayloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bootstrap") {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
			return
		}
		if r.URL.Path != "/agent/chat-history/report" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Service-Key") != "service-secret" {
			t.Fatalf("missing service key header")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		reportPayloads = append(reportPayloads, payload)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"ok": true}})
	}))
	defer server.Close()

	store := session.NewManagerAPIBackend(session.ManagerAPIBackendConfig{
		BaseURL:    server.URL,
		ServiceKey: "service-secret",
		MACAddress: "AA:BB:CC:DD:EE:FF",
		AgentID:    "agent-id",
		SessionID:  "room-1",
	})

	store.AddMessage("livekit:device:AABBCCDDEEFF", "user", "Hello")
	store.AddFullMessage("livekit:device:AABBCCDDEEFF", providers.Message{Role: "assistant", Content: "Hi!"})
	store.AddFullMessage("livekit:device:AABBCCDDEEFF", providers.Message{Role: "tool", Content: "internal tool result"})

	if len(reportPayloads) != 2 {
		t.Fatalf("report payload count = %d, want 2", len(reportPayloads))
	}
	if reportPayloads[0]["sessionId"] != "room-1" || reportPayloads[0]["chatType"].(float64) != 1 {
		t.Fatalf("unexpected user report payload: %+v", reportPayloads[0])
	}
	if reportPayloads[1]["sessionId"] != "room-1" || reportPayloads[1]["chatType"].(float64) != 2 {
		t.Fatalf("unexpected assistant report payload: %+v", reportPayloads[1])
	}
	if got := store.GetHistory("livekit:device:AABBCCDDEEFF"); len(got) != 3 {
		t.Fatalf("local history len = %d, want 3", len(got))
	}
}

func TestManagerAPIBackendPersistsSummary(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"ok": true}})
	}))
	defer server.Close()

	store := session.NewManagerAPIBackend(session.ManagerAPIBackendConfig{
		BaseURL:    strings.TrimRight(server.URL, "/") + "/",
		ServiceKey: "service-secret",
		MACAddress: "AA:BB:CC:DD:EE:FF",
		SessionID:  "room-1",
	})

	store.SetSummary("livekit:device:AABBCCDDEEFF", "new summary")

	if gotPath != "/agent/saveMemory/AA:BB:CC:DD:EE:FF" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotPayload["summaryMemory"] != "new summary" {
		t.Fatalf("summaryMemory payload = %+v", gotPayload)
	}
}

func TestManagerAPIBackendMarksRealtimeChatPersistence(t *testing.T) {
	store := session.NewManagerAPIBackend(session.ManagerAPIBackendConfig{})

	marker, ok := any(store).(session.RealtimeChatPersistenceMarker)
	if !ok {
		t.Fatal("manager backend should implement RealtimeChatPersistenceMarker")
	}
	if !marker.RealtimeChatPersistenceEnabled() {
		t.Fatal("manager backend should mark real-time chat persistence as enabled")
	}
}
