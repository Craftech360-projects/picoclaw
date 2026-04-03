package livekit

import "testing"

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
