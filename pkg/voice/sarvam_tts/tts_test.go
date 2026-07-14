package sarvam_tts

import (
	"context"
	"strings"
	"testing"
)

func TestResolveLanguageCode(t *testing.T) {
	cases := map[string]string{
		"":      "hi-IN",
		"en-IN": "hi-IN",
		"en":    "hi-IN",
		"hi-IN": "hi-IN",
		"ta-IN": "ta-IN",
		"ta":    "ta-IN",
		"TE-in": "te-IN",
		"de-DE": "hi-IN",
		"zz":    "hi-IN",
	}
	for in, want := range cases {
		if got := ResolveLanguageCode(in); got != want {
			t.Errorf("ResolveLanguageCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildWebSocketURL(t *testing.T) {
	got, err := buildWebSocketURL(TTSConfig{BaseURL: "https://api.sarvam.ai", ModelID: "bulbul:v3"})
	if err != nil {
		t.Fatalf("buildWebSocketURL error: %v", err)
	}
	if !strings.HasPrefix(got, "wss://api.sarvam.ai/text-to-speech/ws?") {
		t.Fatalf("URL = %q, want wss scheme + /text-to-speech/ws path", got)
	}
	if !strings.Contains(got, "model=bulbul%3Av3") {
		t.Errorf("URL missing model query: %q", got)
	}
	if !strings.Contains(got, "send_completion_event=true") {
		t.Errorf("URL missing send_completion_event query: %q", got)
	}
}

func TestSynthesizeEmptyKey(t *testing.T) {
	client := NewSarvamTTS(TTSConfig{})
	if _, err := client.Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("expected error for empty api key")
	}
}
