package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSarvamProviderCapabilitiesUseStreaming(t *testing.T) {
	provider := NewSarvamProvider("", "")

	caps := provider.Capabilities()
	if !caps.SupportsStreaming {
		t.Fatal("Sarvam STT should use WebSocket streaming")
	}
	if !caps.SupportsMultilingual {
		t.Fatal("Sarvam STT should support multilingual transcription")
	}
}

func TestSarvamProviderStreamingProtocol(t *testing.T) {
	upgrader := websocket.Upgrader{}
	errCh := make(chan error, 8)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Api-Subscription-Key"); got != "test-sarvam-key" {
			errCh <- fmt.Errorf("Api-Subscription-Key header = %q, want test-sarvam-key", got)
		}

		q := r.URL.Query()
		assertQuery := func(key, want string) {
			if got := q.Get(key); got != want {
				errCh <- fmt.Errorf("query %s = %q, want %q", key, got, want)
			}
		}
		// The realtime endpoint's names. These previously asserted language-code,
		// input_audio_codec, flush_signal, vad_signals and high_vad_sensitivity,
		// which Sarvam accepted and ignored — a green test over a silent socket.
		assertQuery("language_code", "en-IN")
		// The realtime suffix is added even though the manager DB still says
		// saaras:v3; a non-realtime model on this endpoint is another silent mismatch.
		assertQuery("model", "saaras:v3-realtime")
		assertQuery("mode", "transcribe")
		assertQuery("sample_rate", "16000")
		assertQuery("encoding", "linear16")
		assertQuery("stream_type", "balanced")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		// Audio must arrive as a raw binary frame. It used to be base64 inside a JSON
		// text frame, which the real endpoint accepted and never transcribed — the
		// old assertions here passed while production heard nothing.
		audioType, audioData, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if audioType != websocket.BinaryMessage {
			errCh <- fmt.Errorf("audio frame type = %d, want BinaryMessage (%d)", audioType, websocket.BinaryMessage)
		}
		if string(audioData) != "pcm" {
			errCh <- fmt.Errorf("audio payload = %q, want pcm", string(audioData))
		}

		_, flushData, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var flushMsg map[string]string
		if err := json.Unmarshal(flushData, &flushMsg); err != nil {
			errCh <- err
			return
		}
		if got := flushMsg["type"]; got != "flush" {
			errCh <- fmt.Errorf("flush message type = %q, want flush", got)
		}

		if err := conn.WriteJSON(map[string]any{
			"type": "data",
			"data": map[string]any{
				"request_id":    "req_123",
				"transcript":    "hello from sarvam",
				"language_code": "en-IN",
				"metrics": map[string]any{
					"audio_duration":     1.25,
					"processing_latency": 0.2,
				},
			},
		}); err != nil {
			errCh <- err
			return
		}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	t.Setenv("SARVAM_STT_STREAMING_URL", wsURL)

	provider := NewSarvamProvider("test-sarvam-key", "")
	stream, err := provider.OpenStream(context.Background(), StreamOptions{
		SampleRate:    16000,
		Language:      "en",
		EndpointingMS: 500,
	})
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer stream.Close()

	if err := stream.SendAudio([]byte("pcm")); err != nil {
		t.Fatalf("SendAudio failed: %v", err)
	}
	if err := stream.Finalize(); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	evt := nextSarvamEvent(t, stream.Results())
	if evt.Text != "hello from sarvam" || !evt.IsFinal || !evt.SpeechStart || !evt.SpeechEnd {
		t.Fatalf("event = %+v, want final speech event with transcript", evt)
	}
	if evt.Language != "en-IN" {
		t.Fatalf("event language = %q, want en-IN", evt.Language)
	}
	if evt.Duration != 1.25 {
		t.Fatalf("event duration = %v, want 1.25", evt.Duration)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func nextSarvamEvent(t *testing.T, results <-chan TranscriptEvent) TranscriptEvent {
	t.Helper()

	select {
	case evt, ok := <-results:
		if !ok {
			t.Fatal("results channel closed before event")
		}
		return evt
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transcript event")
		return TranscriptEvent{}
	}
}

func TestRealtimeSarvamModel(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", "saaras:v3-realtime"},
		{"saaras:v3", "saaras:v3-realtime"},
		{"saaras:v3-realtime", "saaras:v3-realtime"},
		{"saarika:v2.5", "saarika:v2.5-realtime"},
	} {
		if got := realtimeSarvamModel(tt.in); got != tt.want {
			t.Errorf("realtimeSarvamModel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
