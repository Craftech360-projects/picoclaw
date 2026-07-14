package sarvam_tts

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// TestLiveSynthesize hits the real Sarvam API. It is skipped unless
// SARVAM_API_KEY is set, so it never runs in normal CI.
//
// Run:
//   SARVAM_API_KEY=... go test ./pkg/voice/sarvam_tts/ -run TestLiveSynthesize -v
func TestLiveSynthesize(t *testing.T) {
	apiKey := os.Getenv("SARVAM_API_KEY")
	if apiKey == "" {
		t.Skip("SARVAM_API_KEY not set; skipping live Sarvam synthesis test")
	}

	lang := os.Getenv("SARVAM_TEST_LANGUAGE") // e.g. "hi-IN"; empty -> resolver default
	client := NewSarvamTTS(TTSConfig{
		APIKey:       apiKey,
		VoiceID:      os.Getenv("SARVAM_TEST_VOICE"), // empty -> default meera
		LanguageCode: ResolveLanguageCode(lang),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.Synthesize(ctx, "Hello, welcome to Cheeko!")
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	defer stream.Close()

	var total int
	for {
		chunk, err := stream.Read()
		total += len(chunk)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream error after %d bytes: %v", total, err)
		}
	}
	if total == 0 {
		t.Fatal("received 0 PCM bytes from Sarvam")
	}
	t.Logf("Sarvam returned %d PCM bytes (lang=%s)", total, ResolveLanguageCode(lang))

	if out := os.Getenv("SARVAM_TEST_OUT"); out != "" {
		// Re-run once and dump raw bytes for manual listening if requested.
		s2, err := client.Synthesize(ctx, "Hello, welcome to Cheeko!")
		if err == nil {
			defer s2.Close()
			f, _ := os.Create(out)
			if f != nil {
				defer f.Close()
				for {
					chunk, rerr := s2.Read()
					_, _ = f.Write(chunk)
					if rerr != nil {
						break
					}
				}
			}
		}
	}
}
