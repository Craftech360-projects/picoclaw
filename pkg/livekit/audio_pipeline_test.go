package livekit

import "testing"

func TestSanitizeVoiceTextForTTSDropsProviderChannelMarkers(t *testing.T) {
	got := sanitizeVoiceTextForTTS("<|channel>thought <channel|>I'm sorry, I had trouble.")
	want := "I'm sorry, I had trouble."
	if got != want {
		t.Fatalf("sanitizeVoiceTextForTTS() = %q, want %q", got, want)
	}
}
