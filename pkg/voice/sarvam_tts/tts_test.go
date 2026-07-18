package sarvam_tts

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

func TestResolveLanguageCode(t *testing.T) {
	cases := map[string]string{
		"":      "hi-IN",
		"en-IN": "en-IN",
		"en":    "en-IN",
		"hi-IN": "hi-IN",
		"ta-IN": "ta-IN",
		"ta":    "ta-IN",
		"TE-in": "te-IN",
		"de-DE": "hi-IN",
		"zz":    "hi-IN",
	}
	for in, want := range cases {
		if got := ResolveLanguageCode(in); got != want {
			t.Errorf("ResolveLanguageCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildWebSocketURL(t *testing.T) {
	got, err := buildWebSocketURL(TTSConfig{BaseURL: "https://api.sarvam.ai", ModelID: "bulbul:v3"})
	if err != nil {
		t.Fatalf("buildWebSocketURL error: %v", err)
	}
	if !strings.HasPrefix(got, "wss://api.sarvam.ai/text-to-speech/ws?") {
		t.Fatalf("URL = %q, want wss scheme + /text-to-speech/ws path", got)
	}
	if !strings.Contains(got, "model=bulbul%3Av3") {
		t.Errorf("URL missing model query: %q", got)
	}
	if !strings.Contains(got, "send_completion_event=true") {
		t.Errorf("URL missing send_completion_event query: %q", got)
	}
}

func TestSetLanguage(t *testing.T) {
	client := NewSarvamTTS(TTSConfig{})

	client.SetLanguage("ta-IN")
	if got := client.cfg.LanguageCode; got != "ta-IN" {
		t.Fatalf("after SetLanguage(ta-IN): %q, want ta-IN", got)
	}

	// unknown/auto/empty must not reset a detected language.
	for _, in := range []string{"unknown", "auto", "", "  "} {
		client.SetLanguage(in)
		if got := client.cfg.LanguageCode; got != "ta-IN" {
			t.Fatalf("SetLanguage(%q) changed language to %q, want ta-IN kept", in, got)
		}
	}

	// English keeps its own voice; Hindi maps to hi-IN.
	client.SetLanguage("en-IN")
	if got := client.cfg.LanguageCode; got != "en-IN" {
		t.Fatalf("after SetLanguage(en-IN): %q, want en-IN", got)
	}
	client.SetLanguage("hi-IN")
	if got := client.cfg.LanguageCode; got != "hi-IN" {
		t.Fatalf("after SetLanguage(hi-IN): %q, want hi-IN", got)
	}
}

func TestSynthesizeEmptyKey(t *testing.T) {
	client := NewSarvamTTS(TTSConfig{})
	if _, err := client.Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("expected error for empty api key")
	}
}

// TestSynthesizeReusesConnection verifies the persistent-connection behavior:
// two utterances share one websocket, config is sent once, and each stream
// ends on its own "final" event.
func TestSynthesizeReusesConnection(t *testing.T) {
	var mu sync.Mutex
	conns, configs, texts := 0, 0, 0

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		mu.Lock()
		conns++
		mu.Unlock()
		for {
			var msg struct {
				Type string `json:"type"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			mu.Lock()
			switch msg.Type {
			case "config":
				configs++
			case "text":
				texts++
			case "flush":
				audio := base64.StdEncoding.EncodeToString([]byte("pcm-data"))
				_ = conn.WriteJSON(map[string]any{"type": "audio", "data": map[string]any{"audio": audio}})
				_ = conn.WriteJSON(map[string]any{"type": "event", "data": map[string]any{"event_type": "final"}})
			}
			mu.Unlock()
		}
	}))
	defer server.Close()

	client := NewSarvamTTS(TTSConfig{APIKey: "k", BaseURL: server.URL})

	for i := 0; i < 2; i++ {
		stream, err := client.Synthesize(context.Background(), "hello")
		if err != nil {
			t.Fatalf("Synthesize %d: %v", i, err)
		}
		got := 0
		for {
			b, err := stream.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Read %d: %v", i, err)
			}
			got += len(b)
		}
		stream.Close()
		if got == 0 {
			t.Fatalf("utterance %d: no audio", i)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if conns != 1 {
		t.Fatalf("connections = %d, want 1 (no reuse)", conns)
	}
	if configs != 1 {
		t.Fatalf("config messages = %d, want 1", configs)
	}
	if texts != 2 {
		t.Fatalf("text messages = %d, want 2", texts)
	}
}
