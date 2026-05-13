package livekit

import (
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
)

func TestNewRoomSessionUsesSessionLanguagePolicy(t *testing.T) {
	rs, err := NewRoomSession(RoomSessionConfig{
		RoomInfo:            &lkproto.Room{Name: "room-a"},
		ServerURL:           "ws://localhost:7880",
		SessionLanguageName: "Tamil",
		SessionLanguageCode: "ta-IN",
		PrimaryLanguage:     "English",
	})
	if err != nil {
		t.Fatalf("NewRoomSession() error = %v", err)
	}
	if rs.primaryLanguage != "Tamil" {
		t.Fatalf("primaryLanguage = %q, want Tamil", rs.primaryLanguage)
	}
	if rs.sessionLanguageCode != "ta-IN" {
		t.Fatalf("sessionLanguageCode = %q, want ta-IN", rs.sessionLanguageCode)
	}
}

func TestNewRoomSessionFallsBackToPrimaryLanguageWhenSessionFieldsMissing(t *testing.T) {
	rs, err := NewRoomSession(RoomSessionConfig{
		RoomInfo:        &lkproto.Room{Name: "room-b"},
		ServerURL:       "ws://localhost:7880",
		PrimaryLanguage: "Hindi",
	})
	if err != nil {
		t.Fatalf("NewRoomSession() error = %v", err)
	}
	if rs.primaryLanguage != "Hindi" {
		t.Fatalf("primaryLanguage = %q, want Hindi", rs.primaryLanguage)
	}
	if rs.sessionLanguageCode != "hi" {
		t.Fatalf("sessionLanguageCode = %q, want hi", rs.sessionLanguageCode)
	}
}
