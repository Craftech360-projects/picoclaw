package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	protocol "github.com/livekit/protocol/livekit"
)

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain 12 hex", in: "A1B2C3D4E5F6", want: "a1:b2:c3:d4:e5:f6"},
		{name: "colon separated", in: "A1:B2:C3:D4:E5:F6", want: "a1:b2:c3:d4:e5:f6"},
		{name: "dash separated", in: "a1-b2-c3-d4-e5-f6", want: "a1:b2:c3:d4:e5:f6"},
		{name: "invalid", in: "not-a-mac", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeMAC(tt.in)
			if got != tt.want {
				t.Fatalf("normalizeMAC(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractMACFromRoomName(t *testing.T) {
	room := "62f6d2a2_12AB34CD56EF_conversation"
	got := extractMACFromRoomName(room)
	want := "12:ab:34:cd:56:ef"
	if got != want {
		t.Fatalf("extractMACFromRoomName(%q) = %q, want %q", room, got, want)
	}
}

func TestResolvePersistenceFieldsFromMetadata(t *testing.T) {
	room := "random_room_name"
	metadata := `{"device_mac":"AA11BB22CC33","agent_id":"agent-42"}`

	deviceMAC, agentID := resolvePersistenceFields(room, metadata)
	if deviceMAC != "aa:11:bb:22:cc:33" {
		t.Fatalf("deviceMAC = %q, want %q", deviceMAC, "aa:11:bb:22:cc:33")
	}
	if agentID != "agent-42" {
		t.Fatalf("agentID = %q, want %q", agentID, "agent-42")
	}
}

func TestSendUsageSummaryIncludesTotalTokens(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/token-usage" {
			t.Fatalf("path = %q, want /device/token-usage", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL: server.URL,
		deviceMAC:     "aa:bb:cc:dd:ee:ff",
		roomInfo:      &protocol.Room{Name: "session-1"},
	}

	err := rs.sendUsageSummary(context.Background(), UsageSnapshot{
		InputTokens:            2000,
		OutputTokens:           3000,
		TotalTokens:            5162,
		MessageCount:           8,
		SessionDurationSeconds: 91.25,
	})
	if err != nil {
		t.Fatalf("sendUsageSummary returned error: %v", err)
	}
	if got := payload["totalTokens"]; got != float64(5162) {
		t.Fatalf("totalTokens payload = %#v, want 5162", got)
	}
}

func TestSendSessionSummaryAndEnd(t *testing.T) {
	var summaryPayload map[string]any
	var endPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/summary":
			if r.Method != http.MethodPut {
				t.Fatalf("summary method = %s, want PUT", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&summaryPayload); err != nil {
				t.Fatalf("decode summary payload: %v", err)
			}
		case "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/end":
			if r.Method != http.MethodPost {
				t.Fatalf("end method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&endPayload); err != nil {
				t.Fatalf("decode end payload: %v", err)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Service-Key") != "secret" {
			t.Fatalf("X-Service-Key header = %q", r.Header.Get("X-Service-Key"))
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		agentID:          "agent-1",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}

	if err := rs.sendSessionSummary(context.Background(), "summary text", 4); err != nil {
		t.Fatalf("sendSessionSummary returned error: %v", err)
	}
	if err := rs.sendSessionEnd(context.Background(), 4); err != nil {
		t.Fatalf("sendSessionEnd returned error: %v", err)
	}
	if summaryPayload["summary"] != "summary text" {
		t.Fatalf("summary payload = %+v", summaryPayload)
	}
	if summaryPayload["sourceMessageCount"] != float64(4) {
		t.Fatalf("sourceMessageCount payload = %+v", summaryPayload)
	}
	if endPayload["status"] != "ended" {
		t.Fatalf("end payload = %+v", endPayload)
	}
	if endPayload["messageCount"] != float64(4) {
		t.Fatalf("end messageCount payload = %+v", endPayload)
	}
}
