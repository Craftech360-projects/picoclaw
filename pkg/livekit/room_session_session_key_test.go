package livekit

import (
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
)

func TestSessionKeyForParticipantPrefersDeviceMAC(t *testing.T) {
	rs := &RoomSession{
		roomInfo:   &lkproto.Room{Name: "room-a"},
		deviceMAC:  "aa:11:bb:22:cc:33",
		agentID:    "agent-7",
		agentName:  "test-agent",
		sampleRate: 24000,
	}

	got := rs.sessionKeyForParticipant("participant-a")
	want := "livekit:device:aa11bb22cc33"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}

func TestSessionKeyForParticipantFallsBackToAgentID(t *testing.T) {
	rs := &RoomSession{
		roomInfo: &lkproto.Room{Name: "room-b"},
		agentID:  "agent 42",
	}

	got := rs.sessionKeyForParticipant("participant-b")
	want := "livekit:agent:agent-42"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}

func TestSessionKeyForParticipantFallsBackToRoomAndIdentity(t *testing.T) {
	rs := &RoomSession{
		roomInfo: &lkproto.Room{Name: "room-c"},
	}

	got := rs.sessionKeyForParticipant("participant-c")
	want := "livekit:room-c:participant-c"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}
