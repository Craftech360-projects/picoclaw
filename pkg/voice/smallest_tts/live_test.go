package smallest_tts

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

// TestLiveSmallestSynthesize hits the real smallest.ai Waves WebSocket to prove
// the provider actually synthesizes audio end-to-end. Gated on SMALLEST_API_KEY
// so it never runs in CI without a key.
//
// Run: SMALLEST_API_KEY=... go test ./pkg/voice/smallest_tts/ \
//        -run TestLiveSmallestSynthesize -v -count=1
func TestLiveSmallestSynthesize(t *testing.T) {
	key := os.Getenv("SMALLEST_API_KEY")
	if key == "" {
		t.Skip("SMALLEST_API_KEY not set; skipping live smallest.ai test")
	}

	voice := os.Getenv("SMALLEST_TEST_VOICE")
	if voice == "" {
		voice = "liam" // documented base-queue English voice
	}

	tts := NewSmallestTTS(TTSConfig{
		APIKey:       key,
		VoiceID:      voice,
		ModelID:      "lightning_v3.1",
		OutputFormat: "pcm_24000",
		SampleRateHz: 24000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := tts.Synthesize(ctx, "Hello there! This is a test of Cheeko's voice.")
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	defer stream.Close()

	total, chunks := 0, 0
	for {
		b, err := stream.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Read error after %d bytes/%d chunks: %v", total, chunks, err)
		}
		total += len(b)
		if len(b) > 0 {
			chunks++
		}
	}

	t.Logf("smallest.ai [%s/%s]: received %d audio bytes across %d chunks", "lightning_v3.1", voice, total, chunks)
	if total == 0 {
		t.Fatalf("no audio bytes received — synthesis produced nothing")
	}
}
