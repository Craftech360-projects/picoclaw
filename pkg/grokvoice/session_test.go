// pkg/grokvoice/session_test.go
package grokvoice

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
)

type exec struct{}

func (exec) Execute(_ context.Context, name string, _ map[string]any) (string, bool) {
	return "ran " + name, false
}

func TestGrokSessionSpeaksTheVoiceAgentProtocol(t *testing.T) {
	pcm := []byte{1, 0, 2, 0}
	fromClient := make(chan map[string]any, 16)
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Query().Get("model") != "grok-voice-think-fast-1.0" {
			http.Error(w, "bad auth or model", http.StatusForbidden)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		read := func() map[string]any {
			var m map[string]any
			_ = c.ReadJSON(&m)
			fromClient <- m
			return m
		}
		read() // session.update
		for _, ev := range []map[string]any{
			{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(pcm)},
			{"type": "input_audio_buffer.speech_started"},
			{"type": "conversation.item.input_audio_transcription.completed", "item_id": "u1", "transcript": "what time is it"},
			{"type": "response.output_audio_transcript.delta", "item_id": "a1", "delta": "Let me "},
			{"type": "response.output_audio_transcript.delta", "item_id": "a1", "delta": "check"},
			{"type": "response.function_call_arguments.done", "call_id": "c1", "name": "get_time", "arguments": `{"tz":"IST"}`},
		} {
			_ = c.WriteJSON(ev)
		}
		read() // function_call_output
		read() // response.create
		_ = c.WriteJSON(map[string]any{"type": "response.done", "response": map[string]any{"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}})
		time.Sleep(200 * time.Millisecond) // then the vendor drops the socket
	}))
	defer srv.Close()

	s, err := Dial(context.Background(), Config{
		APIKey: "k", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Instructions: "be kind",
		Tools: []map[string]any{{"type": "function", "name": "get_time"}, {"type": "web_search"}}, Executor: exec{},
	})
	if err != nil {
		t.Fatal(err)
	}

	update := <-fromClient
	session, _ := update["session"].(map[string]any)
	vad, _ := session["turn_detection"].(map[string]any)
	tools, _ := session["tools"].([]any)
	if update["type"] != "session.update" || session["voice"] != "ara" || session["instructions"] != "be kind" ||
		vad["silence_duration_ms"] != float64(700) || len(tools) != 2 {
		t.Fatalf("session.update = %v", update)
	}
	if got := <-s.Audio(); string(got) != string(pcm) {
		t.Fatalf("audio = %v", got)
	}
	if out := <-fromClient; out["type"] != "conversation.item.create" || out["item"].(map[string]any)["output"] != "ran get_time" {
		t.Fatalf("tool output = %v", out)
	}
	if out := <-fromClient; out["type"] != "response.create" {
		t.Fatalf("after tool output = %v", out)
	}

	var events []gptlive.Event
	for ev := range s.Events() {
		events = append(events, ev)
	}
	want := []gptlive.Event{
		gptlive.Interrupted{},
		gptlive.UserTranscript{ID: "u1", Text: "what time is it", Final: true},
		gptlive.AgentTranscript{ID: "a1", Delta: "Let me ", Text: "Let me "},
		gptlive.AgentTranscript{ID: "a1", Delta: "check", Text: "Let me check"},
		gptlive.FunctionCall{CallID: "c1", Name: "get_time", Arguments: `{"tz":"IST"}`},
		gptlive.FunctionResult{CallID: "c1", Name: "get_time", Output: "ran get_time"},
	}
	for i, ev := range want {
		if i >= len(events) || !sameEvent(events[i], ev) {
			t.Fatalf("event %d: got %#v, want %#v", i, events, ev)
		}
	}
	var sawUsage, sawFatal, sawClosed bool
	for _, ev := range events[len(want):] {
		switch e := ev.(type) {
		case gptlive.BackendUsage:
			sawUsage = e.Input == 10 && e.Output == 4 && e.Total == 14
		case gptlive.Error:
			sawFatal = !e.Recoverable
		case gptlive.Closed:
			sawClosed = true
		}
	}
	if !sawUsage || !sawFatal || !sawClosed {
		t.Fatalf("usage=%v fatal=%v closed=%v in %#v", sawUsage, sawFatal, sawClosed, events)
	}
}

// sameEvent compares events, ignoring UserTranscript.StartedAt.
func sameEvent(a, b gptlive.Event) bool {
	if u, ok := a.(gptlive.UserTranscript); ok {
		u.StartedAt = time.Time{}
		a = u
	}
	return a == b
}

func TestGrokCommentaryAndInstructions(t *testing.T) {
	fromClient := make(chan map[string]any, 8)
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 4; i++ {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			fromClient <- m
		}
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "k", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Instructions: "base"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	<-fromClient // initial session.update
	s.AppendCommentary("Greet the child now.")
	s.AppendInstructions("Ask question 2 next.")
	if m := <-fromClient; m["type"] != "conversation.item.create" {
		t.Fatalf("commentary item = %v", m)
	}
	if m := <-fromClient; m["type"] != "response.create" {
		t.Fatalf("commentary response = %v", m)
	}
	m := <-fromClient
	if session, _ := m["session"].(map[string]any); m["type"] != "session.update" || session["instructions"] != "base\n\nAsk question 2 next." {
		t.Fatalf("instructions update = %v", m)
	}
}
