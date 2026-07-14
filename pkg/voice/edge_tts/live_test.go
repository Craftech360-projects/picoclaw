package edge_tts

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// TestLiveSynthesize hits the real (keyless) Edge endpoint. Skipped unless
// EDGE_TTS_LIVE=1, so it never runs in normal CI.
//
// Run:
//   EDGE_TTS_LIVE=1 go test ./pkg/voice/edge_tts/ -run TestLiveSynthesize -v
func TestLiveSynthesize(t *testing.T) {
	if os.Getenv("EDGE_TTS_LIVE") == "" {
		t.Skip("EDGE_TTS_LIVE not set; skipping live Edge synthesis test")
	}

	client := NewEdgeTTS(TTSConfig{VoiceID: os.Getenv("EDGE_TEST_VOICE")}) // empty -> default

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.Synthesize(ctx, "Hello, welcome to Cheeko!")
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	defer stream.Close()

	var total int
	f := createOptional(os.Getenv("EDGE_TEST_OUT"))
	if f != nil {
		defer f.Close()
	}
	for {
		chunk, err := stream.Read()
		total += len(chunk)
		if f != nil {
			_, _ = f.Write(chunk)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream error after %d bytes: %v", total, err)
		}
	}
	if total == 0 {
		t.Fatal("received 0 PCM bytes from Edge")
	}
	t.Logf("Edge returned %d PCM bytes @ %d Hz", total, SampleRate)
}

func createOptional(path string) *os.File {
	if path == "" {
		return nil
	}
	f, _ := os.Create(path)
	return f
}
