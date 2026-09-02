package stt

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// Streams a real PCM file to the real Gemini Live endpoint at wall-clock pace
// and reports every event, so "does the socket answer" is a yes/no question
// instead of an inference from session logs.
//
// Skips unless both are set:
//
//	GEMINI_API_KEY=…  GEMINI_STREAM_PCM=/path/to/16k-mono-s16le.pcm \
//	  go test -run TestGeminiLiveStream -v ./pkg/voice/stt/
func TestGeminiLiveStream(t *testing.T) {
	path := os.Getenv("GEMINI_STREAM_PCM")
	if path == "" || os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_STREAM_PCM or GEMINI_API_KEY not set")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	t.Logf("streaming %s: %d bytes, %.2fs of audio", path, len(data), float64(len(data)/2)/16000)

	stream, err := NewGeminiProvider("", "").OpenStream(context.Background(), StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       os.Getenv("GEMINI_STREAM_LANG"),
		InterimResults: true,
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

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
			t.Logf("EVENT text=%q final=%v lang=%s", evt.Text, evt.IsFinal, evt.Language)
		}
	}()

	// 20ms frames at wall-clock pace: a realtime endpoint may behave differently
	// when handed a whole utterance at once.
	const frameBytes = 640
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
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

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
