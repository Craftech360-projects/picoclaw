package elevenlabs_tts

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSynthesizeUsesWebSocketStreamInput(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var received []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodGet)
		}
		if got := r.URL.Path; got != "/v1/text-to-speech/voice-id/stream-input" {
			t.Fatalf("path = %s, want /v1/text-to-speech/voice-id/stream-input", got)
		}
		if got := r.URL.Query().Get("model_id"); got != "eleven_flash_v2_5" {
			t.Fatalf("model_id = %q, want eleven_flash_v2_5", got)
		}
		if got := r.URL.Query().Get("output_format"); got != "pcm_24000" {
			t.Fatalf("output_format = %q, want pcm_24000", got)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()

		for i := 0; i < 3; i++ {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				t.Fatalf("read websocket message %d: %v", i, err)
			}
			received = append(received, msg)
		}

		if err := conn.WriteJSON(map[string]any{
			"audio":   base64.StdEncoding.EncodeToString([]byte{0x01, 0x02}),
			"isFinal": false,
		}); err != nil {
			t.Fatalf("write audio chunk: %v", err)
		}
		if err := conn.WriteJSON(map[string]any{
			"audio":   base64.StdEncoding.EncodeToString([]byte{0x03, 0x04}),
			"isFinal": false,
		}); err != nil {
			t.Fatalf("write audio chunk: %v", err)
		}
		if err := conn.WriteJSON(map[string]any{"isFinal": true}); err != nil {
			t.Fatalf("write final chunk: %v", err)
		}
	}))
	defer server.Close()

	client := NewElevenLabsTTS(TTSConfig{
		APIKey:       "test-eleven-key",
		VoiceID:      "voice-id",
		ModelID:      "eleven_flash_v2_5",
		OutputFormat: "pcm_24000",
		BaseURL:      server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "hello from picoclaw")
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

	if len(received) != 3 {
		t.Fatalf("received %d websocket messages, want 3", len(received))
	}
	if got := received[0]["text"]; got != " " {
		t.Fatalf("init text = %#v, want single space", got)
	}
	if got := received[0]["xi_api_key"]; got != "test-eleven-key" {
		t.Fatalf("xi_api_key = %#v, want test-eleven-key", got)
	}
	if got := received[1]["text"]; got != "hello from picoclaw" {
		t.Fatalf("text = %#v, want hello from picoclaw", got)
	}
	if got := received[1]["flush"]; got != true {
		t.Fatalf("flush = %#v, want true", got)
	}
	if got := received[2]["text"]; got != "" {
		t.Fatalf("close text = %#v, want empty string", got)
	}
	if string(out) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("audio output mismatch: got %v", out)
	}
}

func TestWebSocketStreamReturnsServerError(t *testing.T) {
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()

		for i := 0; i < 3; i++ {
			_, _, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read websocket message %d: %v", i, err)
			}
		}
		if err := conn.WriteJSON(map[string]any{
			"error": "concurrent_limit_exceeded",
		}); err != nil {
			t.Fatalf("write error chunk: %v", err)
		}
	}))
	defer server.Close()

	client := NewElevenLabsTTS(TTSConfig{
		APIKey:  "test-eleven-key",
		VoiceID: "voice-id",
		BaseURL: server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()

	_, err = stream.Read()
	if err == nil {
		t.Fatal("stream.Read() error = nil, want server error")
	}
}

func TestBuildWebSocketURLEscapesVoiceID(t *testing.T) {
	url, err := buildWebSocketURL(TTSConfig{
		BaseURL:      "https://api.example.test/",
		VoiceID:      "voice/id with space",
		ModelID:      "eleven_flash_v2_5",
		OutputFormat: "pcm_24000",
	})
	if err != nil {
		t.Fatalf("buildWebSocketURL() error = %v", err)
	}

	const want = "wss://api.example.test/v1/text-to-speech/voice%2Fid%20with%20space/stream-input?model_id=eleven_flash_v2_5&output_format=pcm_24000"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
}
