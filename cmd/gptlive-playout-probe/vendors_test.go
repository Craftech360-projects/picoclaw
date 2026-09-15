package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeVendor runs script against the first websocket client and hands back what it read.
func fakeVendor(t *testing.T, script func(c *websocket.Conn, got chan<- map[string]any)) (string, <-chan map[string]any) {
	t.Helper()
	got := make(chan map[string]any, 16)
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		script(c, got)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), got
}

func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Errorf("fake vendor read: %v", err)
	}
	return m
}

func expectAudioAndBargeIn(t *testing.T, s voiceSession, want []byte) {
	t.Helper()
	select {
	case pcm := <-s.Audio():
		if string(pcm) != string(want) {
			t.Fatalf("audio = %v, want %v", pcm, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no model audio")
	}
	select {
	case <-s.Interrupted():
	case <-time.After(2 * time.Second):
		t.Fatal("no barge-in signal")
	}
}

func TestXAISpeaksTheRealtimeProtocol(t *testing.T) {
	pcm := []byte{1, 0, 2, 0}
	url, got := fakeVendor(t, func(c *websocket.Conn, got chan<- map[string]any) {
		got <- readJSON(t, c) // session.update
		_ = c.WriteJSON(map[string]any{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(pcm)})
		_ = c.WriteJSON(map[string]any{"type": "input_audio_buffer.speech_started"})
		for i := 0; i < 3; i++ { // append, item.create, response.create
			got <- readJSON(t, c)
		}
	})
	s, err := dialXAI(context.Background(), dialOptions{key: "k", model: "m", voice: "eve", instructions: "hi", silence: 700 * time.Millisecond, baseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	update := <-got
	session, _ := update["session"].(map[string]any)
	vad, _ := session["turn_detection"].(map[string]any)
	if update["type"] != "session.update" || session["voice"] != "eve" || vad["silence_duration_ms"] != float64(700) {
		t.Fatalf("session.update = %v", update)
	}
	expectAudioAndBargeIn(t, s, pcm)

	s.PushAudio(pcm)
	s.Greet("hello")
	if m := <-got; m["type"] != "input_audio_buffer.append" || m["audio"] != base64.StdEncoding.EncodeToString(pcm) {
		t.Fatalf("append = %v", m)
	}
	if m := <-got; m["type"] != "conversation.item.create" {
		t.Fatalf("greet item = %v", m)
	}
	if m := <-got; m["type"] != "response.create" {
		t.Fatalf("greet response = %v", m)
	}
}

func TestGeminiSpeaksBidiGenerateContent(t *testing.T) {
	pcm := []byte{3, 0, 4, 0}
	url, got := fakeVendor(t, func(c *websocket.Conn, got chan<- map[string]any) {
		got <- readJSON(t, c)                                                       // setup
		_ = c.WriteMessage(websocket.BinaryMessage, []byte(`{"setupComplete":{}}`)) // Gemini sends JSON as binary frames
		content, _ := json.Marshal(map[string]any{"serverContent": map[string]any{
			"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString(pcm)}}}},
		}})
		_ = c.WriteMessage(websocket.BinaryMessage, content)
		_ = c.WriteJSON(map[string]any{"serverContent": map[string]any{"interrupted": true}})
		for i := 0; i < 2; i++ { // realtimeInput audio, greeting
			got <- readJSON(t, c)
		}
	})
	s, err := dialGemini(context.Background(), dialOptions{key: "k", model: "gemini-3.1-flash-live-preview", voice: "Kore", silence: 700 * time.Millisecond, baseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	setup, _ := (<-got)["setup"].(map[string]any)
	gen, _ := setup["generationConfig"].(map[string]any)
	if setup["model"] != "models/gemini-3.1-flash-live-preview" || !strings.Contains(mustJSON(gen), `"voiceName":"Kore"`) {
		t.Fatalf("setup = %v", setup)
	}
	expectAudioAndBargeIn(t, s, pcm)

	s.PushAudio(pcm)
	s.Greet("hello")
	if m := mustJSON(<-got); !strings.Contains(m, `"mimeType":"audio/pcm;rate=16000"`) {
		t.Fatalf("realtime audio = %s", m)
	}
	if m := mustJSON(<-got); m != `{"realtimeInput":{"text":"hello"}}` { // 3.1 refuses clientContent after setup
		t.Fatalf("3.1 greeting = %s", m)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
