// pkg/geminilive/session_test.go
package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
)

type exec struct{}

func (exec) Execute(_ context.Context, name string, args map[string]any) (string, bool) {
	return name + "=" + args["tz"].(string), false
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestGeminiSessionToolsTranscriptsAndResumption(t *testing.T) {
	pcm := []byte{3, 0, 4, 0}
	setups := make(chan map[string]any, 2)
	toolResponses := make(chan map[string]any, 1)
	var conns atomic.Int32
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "g" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var setup map[string]any
		_ = c.ReadJSON(&setup)
		setups <- setup["setup"].(map[string]any)
		send := func(v any) { _ = c.WriteMessage(websocket.BinaryMessage, []byte(mustJSON(v))) } // Gemini frames JSON as binary
		send(map[string]any{"setupComplete": map[string]any{}})
		audio := map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{
			"parts": []any{map[string]any{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString(pcm)}}},
		}}}
		if conns.Add(1) == 1 {
			send(map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "what time "}}})
			send(map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]any{"text": "is it"}}})
			send(map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "f1", "name": "get_time", "args": map[string]any{"tz": "IST"}}}}})
			var resp map[string]any
			_ = c.ReadJSON(&resp)
			toolResponses <- resp
			send(audio)
			send(map[string]any{"serverContent": map[string]any{"outputTranscription": map[string]any{"text": "It is noon."}}})
			send(map[string]any{"serverContent": map[string]any{"turnComplete": true}})
			send(map[string]any{"usageMetadata": map[string]any{"promptTokenCount": 20, "responseTokenCount": 5, "totalTokenCount": 25}})
			send(map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": "h1", "resumable": true}})
			return // server drops the connection: the client must resume with h1
		}
		send(audio)
		send(map[string]any{"serverContent": map[string]any{"interrupted": true}})
		time.Sleep(time.Second)
	}))
	defer srv.Close()

	s, err := Dial(context.Background(), Config{
		APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Model: "gemini-3.1-flash-live-preview", Voice: "Kore",
		Instructions: "be kind", Executor: exec{},
		Tools: []map[string]any{{"type": "function", "name": "get_time", "description": "time", "parameters": map[string]any{"type": "object"}}, {"type": "web_search"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	first := mustJSON(<-setups)
	for _, want := range []string{`"model":"models/gemini-3.1-flash-live-preview"`, `"voiceName":"Kore"`, `"silenceDurationMs":700`,
		`"functionDeclarations":[{"description":"time","name":"get_time","parametersJsonSchema":{"type":"object"}}]`,
		`"googleSearch":{}`, `"inputAudioTranscription":{}`, `"outputAudioTranscription":{}`, `"slidingWindow":{}`, `"sessionResumption":{}`} {
		if !strings.Contains(first, want) {
			t.Fatalf("first setup %s lacks %s", first, want)
		}
	}
	if resp := mustJSON(<-toolResponses); resp != `{"toolResponse":{"functionResponses":[{"id":"f1","name":"get_time","response":{"output":"get_time=IST"}}]}}` {
		t.Fatalf("toolResponse = %s", resp)
	}
	if got := <-s.Audio(); string(got) != string(pcm) {
		t.Fatalf("audio = %v", got)
	}
	if second := mustJSON(<-setups); !strings.Contains(second, `"sessionResumption":{"handle":"h1"}`) {
		t.Fatalf("resumed setup = %s", second)
	}
	if got := <-s.Audio(); string(got) != string(pcm) {
		t.Fatalf("audio after resume = %v", got)
	}

	var user gptlive.UserTranscript
	var agent gptlive.AgentTranscript
	var usage gptlive.BackendUsage
	var sawCall, sawResult, sawInterrupt bool
	deadline := time.After(3 * time.Second)
	for !(sawInterrupt && user.Final && sawResult) {
		select {
		case ev := <-s.Events():
			switch e := ev.(type) {
			case gptlive.UserTranscript:
				user = e
			case gptlive.AgentTranscript:
				agent = e
			case gptlive.BackendUsage:
				usage = e
			case gptlive.FunctionCall:
				sawCall = e.Name == "get_time" && e.Arguments == `{"tz":"IST"}`
			case gptlive.FunctionResult:
				sawResult = e.Output == "get_time=IST" && !e.IsError
			case gptlive.Interrupted:
				sawInterrupt = true
			}
		case <-deadline:
			t.Fatalf("missing events: user=%+v result=%v interrupt=%v", user, sawResult, sawInterrupt)
		}
	}
	if user.Text != "what time is it" || !sawCall || agent.Text != "It is noon." || usage.Total != 25 {
		t.Fatalf("user=%+v call=%v agent=%+v usage=%+v", user, sawCall, agent, usage)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func nextEvent(t *testing.T, s *Session) gptlive.Event {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an event")
		return nil
	}
}

// Input transcription often trails the model's first audio: the fragments of one utterance must
// still become exactly one final user transcript, emitted when the model turn ends.
func TestGeminiOneFinalUserTranscriptPerTurn(t *testing.T) {
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.ReadMessage() // setup
		send := func(v any) { _ = c.WriteMessage(websocket.BinaryMessage, []byte(mustJSON(v))) }
		content := func(v map[string]any) { send(map[string]any{"serverContent": v}) }
		send(map[string]any{"setupComplete": map[string]any{}})
		content(map[string]any{"inputTranscription": map[string]any{"text": "what time "}})
		content(map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte{1, 0})}}}}})
		content(map[string]any{"outputTranscription": map[string]any{"text": "It is"}})
		content(map[string]any{"inputTranscription": map[string]any{"text": "is it"}})
		content(map[string]any{"outputTranscription": map[string]any{"text": " noon."}})
		content(map[string]any{"generationComplete": true})
		content(map[string]any{"turnComplete": true})
		send(map[string]any{"usageMetadata": map[string]any{"totalTokenCount": 1}})
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	var users []gptlive.UserTranscript
	agentText := ""
	for done := false; !done; {
		switch e := nextEvent(t, s).(type) {
		case gptlive.UserTranscript:
			if agentText != "It is noon." {
				t.Fatalf("user transcript %+v emitted before the model turn ended (agent so far %q)", e, agentText)
			}
			users = append(users, e)
		case gptlive.AgentTranscript:
			if len(users) > 0 {
				t.Fatalf("agent transcript %+v after the user final", e)
			}
			agentText = e.Text
		case gptlive.BackendUsage:
			done = true // sent after turnComplete: the user final must already be out
		}
	}
	if len(users) != 1 || users[0].Text != "what time is it" || !users[0].Final {
		t.Fatalf("user transcripts = %+v, want one final \"what time is it\"", users)
	}
}

func TestGeminiDialErrorScrubsRawAndEscapedKey(t *testing.T) {
	const key = "a+b/c=d"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// echo the key back in both forms, as a vendor error body might
		http.Error(w, "bad request "+r.URL.RawQuery+" "+r.URL.Query().Get("key"), http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), Config{APIKey: key, BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err == nil {
		t.Fatal("dial succeeded against a refusing server")
	}
	if msg := err.Error(); strings.Contains(msg, key) || strings.Contains(msg, url.QueryEscape(key)) {
		t.Fatalf("error leaks the key: %s", msg)
	}
}

func TestGeminiCommentaryPerModelFamily(t *testing.T) {
	for model, want := range map[string]string{
		"gemini-3.1-flash-live-preview":                 `{"realtimeInput":{"text":"hi"}}`,
		"gemini-2.5-flash-native-audio-preview-12-2025": `{"clientContent":{"turnComplete":true,"turns":[{"parts":[{"text":"hi"}],"role":"user"}]}}`,
	} {
		sent := make(chan string, 4)
		var up websocket.Upgrader
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			_, _, _ = c.ReadMessage() // setup
			_ = c.WriteJSON(map[string]any{"setupComplete": map[string]any{}})
			_, msg, _ := c.ReadMessage()
			sent <- string(msg)
		}))
		s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Model: model})
		if err != nil {
			t.Fatal(err)
		}
		s.AppendCommentary("hi")
		if got := strings.TrimSpace(<-sent); got != want {
			t.Fatalf("%s commentary = %s, want %s", model, got, want)
		}
		_ = s.Close(context.Background())
		srv.Close()
	}
}
