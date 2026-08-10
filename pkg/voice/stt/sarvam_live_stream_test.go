package stt

import (
	"context"
	"os"
	"sync"
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
	var mu sync.Mutex
	var events, finals int
	go func() {
		defer close(done)
		for evt := range stream.Results() {
			mu.Lock()
			events++
			if evt.IsFinal {
				finals++
			}
			mu.Unlock()
			t.Logf("EVENT text=%q final=%v speech_start=%v speech_end=%v lang=%s dur=%.2f",
				evt.Text, evt.IsFinal, evt.SpeechStart, evt.SpeechEnd, evt.Language, evt.Duration)
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

	// Wait for a final, not merely for any event. An earlier version returned 3.5s
	// after the first event, which cut off finals that arrive later — language
	// auto-detect is slower than a pinned language — and made "auto produces no
	// finals" look like a provider behaviour rather than a measurement artifact.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			mu.Lock()
			e, f := events, finals
			mu.Unlock()
			if e == 0 {
				t.Fatal("no events in 30s after flush — the socket accepted audio and answered nothing")
			}
			t.Logf("done: %d events, %d finals (no final within 30s)", e, f)
			return
		case <-time.After(250 * time.Millisecond):
			mu.Lock()
			e, f := events, finals
			mu.Unlock()
			if f > 0 {
				t.Logf("done: %d events, %d finals", e, f)
				return
			}
			_ = e
		}
	}
}
