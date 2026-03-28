package cartesia_tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSynthesizeSuccess(t *testing.T) {
	type outputFormat struct {
		Container  string `json:"container"`
		Encoding   string `json:"encoding"`
		SampleRate int    `json:"sample_rate"`
	}
	type requestBody struct {
		ModelID      string         `json:"model_id"`
		Transcript   string         `json:"transcript"`
		Voice        map[string]any `json:"voice"`
		OutputFormat outputFormat   `json:"output_format"`
		Language     string         `json:"language"`
	}

	var received requestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodPost)
		}
		if got := r.URL.Path; got != "/tts/bytes" {
			t.Fatalf("path = %s, want /tts/bytes", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-cartesia-key" {
			t.Fatalf("X-API-Key = %q, want %q", got, "test-cartesia-key")
		}
		if got := r.Header.Get("Cartesia-Version"); got != "2025-04-16" {
			t.Fatalf("Cartesia-Version = %q, want %q", got, "2025-04-16")
		}

		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x01, 0x02, 0x03, 0x04})
	}))
	defer server.Close()

	client := NewCartesiaTTS(TTSConfig{
		APIKey:       "test-cartesia-key",
		VoiceID:      "voice-id",
		ModelID:      "sonic-3",
		SampleRateHz: 24000,
		Language:     "en",
		BaseURL:      server.URL,
		APIVersion:   "2025-04-16",
	})

	stream, err := client.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()

	var out []byte
	for {
		chunk, readErr := stream.Read()
		if len(chunk) > 0 {
			out = append(out, chunk...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("stream.Read() error = %v", readErr)
		}
	}

	if received.ModelID != "sonic-3" {
		t.Fatalf("model_id = %q, want %q", received.ModelID, "sonic-3")
	}
	if received.Transcript != "hello" {
		t.Fatalf("transcript = %q, want %q", received.Transcript, "hello")
	}
	if received.Language != "en" {
		t.Fatalf("language = %q, want %q", received.Language, "en")
	}
	if received.OutputFormat.Container != "raw" {
		t.Fatalf("container = %q, want %q", received.OutputFormat.Container, "raw")
	}
	if received.OutputFormat.Encoding != "pcm_s16le" {
		t.Fatalf("encoding = %q, want %q", received.OutputFormat.Encoding, "pcm_s16le")
	}
	if received.OutputFormat.SampleRate != 24000 {
		t.Fatalf("sample_rate = %d, want %d", received.OutputFormat.SampleRate, 24000)
	}
	if string(out) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("audio output mismatch: got %v", out)
	}
}

func TestSynthesizeBearerHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer token-123")
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Fatalf("X-API-Key = %q, want empty", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x00, 0x00})
	}))
	defer server.Close()

	client := NewCartesiaTTS(TTSConfig{
		APIKey:  "Bearer token-123",
		VoiceID: "voice-id",
		BaseURL: server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "test")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	_ = stream.Close()
}
