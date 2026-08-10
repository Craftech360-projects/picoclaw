package stt

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

// Streams a real PCM file to the real Sarvam realtime endpoint at wall-clock pace
// and reports every event, so "does the socket ever answer" is a question with a
// yes/no answer instead of an inference from session logs.
//
// Skips unless both are set:
//
//	SARVAM_API_KEY=…  SARVAM_STREAM_PCM=/path/to/16k-mono-s16le.pcm \
//	  go test -run TestSarvamLiveStream -v ./pkg/voice/stt/
func TestSarvamLiveStream(t *testing.T) {
	path := os.Getenv("SARVAM_STREAM_PCM")
	if path == "" || os.Getenv("SARVAM_API_KEY") == "" {
		t.Skip("SARVAM_STREAM_PCM or SARVAM_API_KEY not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	seconds := float64(len(data)/2) / 16000
	t.Logf("streaming %s: %d bytes, %.2fs of audio", path, len(data), seconds)

	provider := NewSarvamProvider("", "")
	stream, err := provider.OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       os.Getenv("SARVAM_STREAM_LANG"),
		InterimResults: true,
		EndpointingMS:  1000,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	// Collect in the background so interim results are visible as they land rather
	// than only after the flush.
	done := make(chan struct{})
	var events int
	go func() {
		defer close(done)
		for evt := range stream.Results() {
			events++
			t.Logf("EVENT #%d text=%q final=%v speech_start=%v speech_end=%v lang=%s dur=%.2f",
				events, evt.Text, evt.IsFinal, evt.SpeechStart, evt.SpeechEnd, evt.Language, evt.Duration)
		}
	}()

	// 20ms frames at wall-clock pace: a realtime endpoint is entitled to behave
	// differently when handed an entire utterance at once.
	const frameBytes = 640
	started := time.Now()
	for off := 0; off < len(data); off += frameBytes {
		end := off + frameBytes
		if end > len(data) {
			end = len(data)
		}
		if err := stream.SendAudio(data[off:end]); err != nil {
			t.Fatalf("SendAudio at offset %d: %v", off, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("sent all audio in %v, sending flush", time.Since(started))

	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Wait well past any plausible processing latency before calling it silence.
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			if events == 0 {
				t.Fatal("no events in 20s after flush — the socket accepted audio and answered nothing")
			}
			t.Logf("done: %d events", events)
			return
		case <-time.After(500 * time.Millisecond):
			if events > 0 {
				// Keep listening briefly for a final after an interim.
				time.Sleep(3 * time.Second)
				t.Logf("done: %d events", events)
				return
			}
		}
	}
}

// silenceGuard keeps the import of binary used if the frame loop is edited to
// synthesise audio instead of reading a file.
var _ = binary.LittleEndian
