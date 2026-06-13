package deepgram_tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSynthesizeUsesDeepgramWebSocketSpeak(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var received []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodGet)
		}
		if got := r.URL.Path; got != "/v1/speak" {
			t.Fatalf("path = %s, want /v1/speak", got)
		}
		if got := r.Header.Get("Authorization"); got != "Token test-deepgram-key" {
			t.Fatalf("Authorization = %q, want Token test-deepgram-key", got)
		}
		if got := r.URL.Query().Get("model"); got != "aura-2-asteria-en" {
			t.Fatalf("model = %q, want aura-2-asteria-en", got)
		}
		if got := r.URL.Query().Get("encoding"); got != "linear16" {
			t.Fatalf("encoding = %q, want linear16", got)
		}
		if got := r.URL.Query().Get("sample_rate"); got != "24000" {
			t.Fatalf("sample_rate = %q, want 24000", got)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()

		for i := 0; i < 2; i++ {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				t.Fatalf("read websocket message %d: %v", i, err)
			}
			received = append(received, msg)
		}

		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"Metadata","request_id":"req-1","model_name":"aura-2-asteria-en","model_version":"v","model_uuid":"00000000-0000-0000-0000-000000000000"}`)); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02}); err != nil {
			t.Fatalf("write audio chunk: %v", err)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x03, 0x04}); err != nil {
			t.Fatalf("write audio chunk: %v", err)
		}
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			t.Fatalf("write close: %v", err)
		}
	}))
	defer server.Close()

	client := NewDeepgramTTS(TTSConfig{
		APIKey:       "test-deepgram-key",
		ModelID:      "aura-2-asteria-en",
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

	if len(received) != 2 {
		t.Fatalf("received %d websocket messages, want 2", len(received))
	}
	if got := received[0]["type"]; got != "Speak" {
		t.Fatalf("first type = %#v, want Speak", got)
	}
	if got := received[0]["text"]; got != "hello from picoclaw" {
		t.Fatalf("text = %#v, want hello from picoclaw", got)
	}
	if got := received[1]["type"]; got != "Close" {
		t.Fatalf("second type = %#v, want Close", got)
	}
	if string(out) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("audio output mismatch: got %v", out)
	}
}

func TestSynthesizeAcceptsBearerToken(t *testing.T) {
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-123" {
			t.Fatalf("Authorization = %q, want Bearer jwt-123", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			_, _, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read websocket message %d: %v", i, err)
			}
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}))
	defer server.Close()

	client := NewDeepgramTTS(TTSConfig{
		APIKey:  "Bearer jwt-123",
		ModelID: "aura-2-asteria-en",
		BaseURL: server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "test")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	_ = stream.Close()
}

func TestBuildWebSocketURLUsesVoiceIDAsModelFallback(t *testing.T) {
	url, err := buildWebSocketURL(TTSConfig{
		BaseURL:      "https://api.example.test/",
		VoiceID:      "aura-2-hera-en",
		OutputFormat: "pcm_16000",
	})
	if err != nil {
		t.Fatalf("buildWebSocketURL() error = %v", err)
	}

	const want = "wss://api.example.test/v1/speak?encoding=linear16&model=aura-2-hera-en&sample_rate=16000"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
}
