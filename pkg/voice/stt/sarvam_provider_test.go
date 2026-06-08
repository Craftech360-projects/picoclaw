package stt

import (
	"context"
	"encoding/base64"
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
		assertQuery("language-code", "en-IN")
		assertQuery("model", "saaras:v3")
		assertQuery("mode", "transcribe")
		assertQuery("sample_rate", "16000")
		assertQuery("input_audio_codec", "pcm_s16le")
		assertQuery("flush_signal", "true")
		assertQuery("vad_signals", "true")
		assertQuery("high_vad_sensitivity", "true")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		_, audioData, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var audioMsg struct {
			Audio struct {
				Data       string `json:"data"`
				SampleRate int    `json:"sample_rate"`
				Encoding   string `json:"encoding"`
			} `json:"audio"`
		}
		if err := json.Unmarshal(audioData, &audioMsg); err != nil {
			errCh <- err
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(audioMsg.Audio.Data)
		if err != nil {
			errCh <- err
			return
		}
		if string(decoded) != "pcm" {
			errCh <- fmt.Errorf("audio payload = %q, want pcm", string(decoded))
		}
		if audioMsg.Audio.SampleRate != 16000 {
			errCh <- fmt.Errorf("audio sample_rate = %d, want 16000", audioMsg.Audio.SampleRate)
		}
		if audioMsg.Audio.Encoding != "audio/wav" {
			errCh <- fmt.Errorf("audio encoding = %q, want audio/wav", audioMsg.Audio.Encoding)
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
