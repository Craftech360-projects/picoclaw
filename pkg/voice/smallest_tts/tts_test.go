package smallest_tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSynthesizeUsesSmallestWebSocketChunks(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var received map[string]any
	wantAudio := []byte{0x11, 0x22, 0x33}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want %s", r.Method, http.MethodGet)
		}
		if got := r.URL.Path; got != "/waves/v1/tts/live" {
			t.Fatalf("path = %s, want /waves/v1/tts/live", got)
		}
		if got := r.URL.Query().Get("timeout"); got != "120" {
			t.Fatalf("timeout query = %q, want 120", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-smallest-key" {
			t.Fatalf("Authorization = %q, want Bearer test-smallest-key", got)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()

		if err := conn.ReadJSON(&received); err != nil {
			t.Fatalf("read websocket request: %v", err)
		}

		chunkFrame, err := json.Marshal(map[string]any{
			"status": "chunk",
			"data": map[string]any{
				"audio": base64.StdEncoding.EncodeToString(wantAudio),
			},
		})
		if err != nil {
			t.Fatalf("marshal chunk frame: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, chunkFrame); err != nil {
			t.Fatalf("write chunk frame: %v", err)
		}

		completeFrame, err := json.Marshal(map[string]any{"status": "complete"})
		if err != nil {
			t.Fatalf("marshal complete frame: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, completeFrame); err != nil {
			t.Fatalf("write complete frame: %v", err)
		}
	}))
	defer server.Close()

	client := NewSmallestTTS(TTSConfig{
		APIKey:       "test-smallest-key",
		VoiceID:      "liam",
		ModelID:      "lightning_v3.1",
		OutputFormat: "pcm_24000",
		BaseURL:      server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "hello from picoclaw")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()

	chunk, err := stream.Read()
	if err != nil {
		t.Fatalf("stream.Read() error = %v", err)
	}
	if string(chunk) != string(wantAudio) {
		t.Fatalf("audio output mismatch: got %v, want %v", chunk, wantAudio)
	}

	if _, err := stream.Read(); err != io.EOF {
		t.Fatalf("second stream.Read() error = %v, want io.EOF", err)
	}

	if got := received["voice_id"]; got != "liam" {
		t.Fatalf("voice_id = %#v, want liam", got)
	}
	if got := received["text"]; got != "hello from picoclaw" {
		t.Fatalf("text = %#v, want hello from picoclaw", got)
	}
	if got := received["model"]; got != "lightning_v3.1" {
		t.Fatalf("model = %#v, want lightning_v3.1", got)
	}
	if got := received["sample_rate"]; got != float64(24000) {
		t.Fatalf("sample_rate = %#v, want 24000", got)
	}
	if got := received["flush"]; got != true {
		t.Fatalf("flush = %#v, want true", got)
	}
	if _, ok := received["continue"]; ok {
		t.Fatalf("request should not set continue, got %#v", received["continue"])
	}
}

func TestSynthesizeDefaultsVoiceAndModel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var received map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()
		if err := conn.ReadJSON(&received); err != nil {
			t.Fatalf("read websocket request: %v", err)
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}))
	defer server.Close()

	client := NewSmallestTTS(TTSConfig{
		APIKey:  "test-smallest-key",
		BaseURL: server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "test")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()

	if _, err := stream.Read(); err != io.EOF {
		t.Fatalf("stream.Read() error = %v, want io.EOF", err)
	}

	if got := received["voice_id"]; got != "liam" {
		t.Fatalf("voice_id = %#v, want default liam", got)
	}
	if got := received["model"]; got != "lightning_v3.1" {
		t.Fatalf("model = %#v, want default lightning_v3.1", got)
	}
}

func TestSynthesizeErrorFrame(t *testing.T) {
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer conn.Close()
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read websocket request: %v", err)
		}
		errFrame, _ := json.Marshal(map[string]any{"status": "error", "message": "boom"})
		_ = conn.WriteMessage(websocket.TextMessage, errFrame)
	}))
	defer server.Close()

	client := NewSmallestTTS(TTSConfig{
		APIKey:  "test-smallest-key",
		BaseURL: server.URL,
	})

	stream, err := client.Synthesize(context.Background(), "test")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()

	_, err = stream.Read()
	if err == nil {
		t.Fatalf("stream.Read() error = nil, want error")
	}
	if want := "boom"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want to contain %q", err.Error(), want)
	}
}

func TestBuildWebSocketURLDefaults(t *testing.T) {
	url, err := buildWebSocketURL(TTSConfig{BaseURL: "https://api.smallest.ai"})
	if err != nil {
		t.Fatalf("buildWebSocketURL() error = %v", err)
	}
	const want = "wss://api.smallest.ai/waves/v1/tts/live?timeout=120"
	if url != want {
		t.Fatalf("url = %q, want %q", url, want)
	}
}
