package main

import "testing"

// The cron key must land on the same file as the voice session it belongs to.
// The literal below is the one pkg/livekit's
// TestSessionKeyForParticipantSeparatesCharactersOnOneDevice builds from the
// same MAC and character id; if either side drifts, a scheduled task starts
// writing to a transcript nobody reads.
func TestLivekitCronSessionKeyCarriesTheCharacter(t *testing.T) {
	got := livekitCronSessionKey("aa:11:bb:22:cc:33", "11111111-1111-1111-1111-111111111111", "room-a")
	want := "livekit:device:aa11bb22cc33:agent:11111111-1111-1111-1111-111111111111"
	if got != want {
		t.Fatalf("livekitCronSessionKey() = %q, want %q", got, want)
	}
}

func TestLivekitCronSessionKeyWithoutAgentIDIsUnchanged(t *testing.T) {
	got := livekitCronSessionKey("aa:11:bb:22:cc:33", "", "room-a")
	want := "livekit:device:aa11bb22cc33"
	if got != want {
		t.Fatalf("livekitCronSessionKey() = %q, want %q", got, want)
	}
}

func TestLivekitCronSessionKeyFallbacksAreUnchanged(t *testing.T) {
	if got, want := livekitCronSessionKey("", "agent 42", "room-b"), "livekit:agent:agent-42"; got != want {
		t.Fatalf("agent fallback = %q, want %q", got, want)
	}
	if got, want := livekitCronSessionKey("", "", "room-c"), "livekit:room-c:cron"; got != want {
		t.Fatalf("room fallback = %q, want %q", got, want)
	}
}
