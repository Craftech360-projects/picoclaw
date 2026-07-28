package elevenlabs_tts

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// TestLiveEmotionSamples synthesizes the same sentence at each emotion bucket and
// writes WAVs so the delivery can be compared by ear. Skipped unless
// ELEVENLABS_API_KEY is set. Override the voice with ELEVENLABS_TEST_VOICE_ID and
// the output dir with ELEVENLABS_TEST_OUT_DIR.
func TestLiveEmotionSamples(t *testing.T) {
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		t.Skip("ELEVENLABS_API_KEY not set")
	}

	voiceID := os.Getenv("ELEVENLABS_TEST_VOICE_ID")
	if voiceID == "" {
		voiceID = "21m00Tcm4TlvDq8ikWAM" // premade "Rachel", available to all accounts
	}
	outDir := os.Getenv("ELEVENLABS_TEST_OUT_DIR")
	if outDir == "" {
		outDir = "."
	}

	const sampleRate = 24000
	client := NewElevenLabsTTS(TTSConfig{
		APIKey:       apiKey,
		VoiceID:      voiceID,
		ModelID:      "eleven_turbo_v2_5",
		OutputFormat: "pcm_24000",
	})

	const text = "Wow, we found the secret treasure map! Can you believe it?"

	for _, emotion := range []string{"neutral", "excited", "sleepy"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		synthCtx := ctx
		if emotion != "neutral" {
			synthCtx = tts.WithEmotion(ctx, emotion)
		}

		vs := voiceSettings(synthCtx)
		t.Logf("%-8s stability=%.2f style=%.2f", emotion, vs["stability"], vs["style"])

		stream, err := client.Synthesize(synthCtx, text)
		if err != nil {
			cancel()
			t.Fatalf("synthesize %s: %v", emotion, err)
		}

		var pcm []byte
		for {
			chunk, err := stream.Read()
			pcm = append(pcm, chunk...)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				stream.Close()
				cancel()
				t.Fatalf("read %s after %d bytes: %v", emotion, len(pcm), err)
			}
		}
		stream.Close()
		cancel()

		if len(pcm) == 0 {
			t.Fatalf("%s produced no audio", emotion)
		}

		path := filepath.Join(outDir, "elevenlabs_"+emotion+".wav")
		if err := os.WriteFile(path, wrapWAV(pcm, sampleRate), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("%-8s wrote %s (%d bytes pcm, %.2fs)", emotion, path, len(pcm), float64(len(pcm))/2/sampleRate)
	}
}

// wrapWAV prepends a 44-byte RIFF header to mono 16-bit PCM so the file is playable.
func wrapWAV(pcm []byte, sampleRate int) []byte {
	const numChannels, bitsPerSample = 1, 16
	byteRate := sampleRate * numChannels * bitsPerSample / 8

	buf := make([]byte, 0, 44+len(pcm))
	put32 := func(v uint32) { buf = binary.LittleEndian.AppendUint32(buf, v) }
	put16 := func(v uint16) { buf = binary.LittleEndian.AppendUint16(buf, v) }

	buf = append(buf, "RIFF"...)
	put32(uint32(36 + len(pcm)))
	buf = append(buf, "WAVEfmt "...)
	put32(16)                              // fmt chunk size
	put16(1)                               // PCM
	put16(numChannels)                     //
	put32(uint32(sampleRate))              //
	put32(uint32(byteRate))                //
	put16(numChannels * bitsPerSample / 8) // block align
	put16(bitsPerSample)                   //
	buf = append(buf, "data"...)
	put32(uint32(len(pcm)))
	return append(buf, pcm...)
}
