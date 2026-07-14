package azure_tts

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSynthesize hits the real Azure Speech API. Skipped unless both
// AZURE_SPEECH_KEY and AZURE_SPEECH_REGION are set.
//
// Run:
//   AZURE_SPEECH_KEY=... AZURE_SPEECH_REGION=eastus \
//     go test ./pkg/voice/azure_tts/ -run TestLiveSynthesize -v
func TestLiveSynthesize(t *testing.T) {
	key := os.Getenv("AZURE_SPEECH_KEY")
	region := strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION"))
	if key == "" || region == "" {
		t.Skip("AZURE_SPEECH_KEY/AZURE_SPEECH_REGION not set; skipping live Azure test")
	}

	client := NewAzureTTS(TTSConfig{
		APIKey:       key,
		VoiceID:      os.Getenv("AZURE_TEST_VOICE"), // empty -> default en-US-AnaNeural
		Endpoint:     "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/v1",
		SampleRateHz: 24000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := client.Synthesize(ctx, "Hello, welcome to Cheeko!")
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	defer stream.Close()

	var total int
	f := createOptional(os.Getenv("AZURE_TEST_OUT"))
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
		t.Fatal("received 0 PCM bytes from Azure")
	}
	t.Logf("Azure returned %d PCM bytes @ 24000 Hz", total)
}

func createOptional(path string) *os.File {
	if path == "" {
		return nil
	}
	f, _ := os.Create(path)
	return f
}
