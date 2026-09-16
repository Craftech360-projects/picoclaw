// pkg/geminilive/session_test.go
package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/realtimeconn"
)

// testTimeout bounds blocking receives in these tests so a regression fails
// fast instead of hanging to the 10-minute go test default (see grokvoice's
// session_test.go, which uses the same pattern).
const testTimeout = 5 * time.Second

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
	defer func(d time.Duration) { userFlushDelay = d }(userFlushDelay)
	userFlushDelay = time.Hour // only the turn end may flush in this test
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

// Google Search runs server-side (no toolCall), so the model can go quiet mid-turn long enough for
// the pipeline to persist the agent's text: the user final must not wait for the turn end.
func TestGeminiUserTranscriptFlushesBeforeTurnEndDuringServerSideWork(t *testing.T) {
	defer func(d time.Duration) { userFlushDelay = d }(userFlushDelay)
	userFlushDelay = 10 * time.Millisecond
	release := make(chan struct{})
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
		content(map[string]any{"inputTranscription": map[string]any{"text": "any news today"}})
		content(map[string]any{"outputTranscription": map[string]any{"text": "Let me look."}})
		select { // the search is running: no turn end until the test has seen the user final
		case <-release:
		case <-time.After(5 * time.Second):
			return
		}
		content(map[string]any{"outputTranscription": map[string]any{"text": " Sunny."}})
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

	for {
		if e, ok := nextEvent(t, s).(gptlive.UserTranscript); ok {
			if e.Text != "any news today" || !e.Final {
				t.Fatalf("user transcript = %+v", e)
			}
			break
		}
	}
	close(release)
	for {
		switch e := nextEvent(t, s).(type) {
		case gptlive.UserTranscript:
			t.Fatalf("second user transcript %+v", e)
		case gptlive.BackendUsage:
			return
		}
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
		"gemini-3.8-live":                               `{"realtimeInput":{"text":"hi"}}`,
		"gemini-3.8-live-extended-thinking":             `{"realtimeInput":{"text":"hi"}}`,
		"gemini-9.9-live-not-invented-yet":              `{"realtimeInput":{"text":"hi"}}`,
		"gemini-2.5-flash-native-audio-preview-12-2025": `{"clientContent":{"turnComplete":true,"turns":[{"parts":[{"text":"hi"}],"role":"user"}]}}`,
		"gemini-2.5-pro-live-preview":                   `{"clientContent":{"turnComplete":true,"turns":[{"parts":[{"text":"hi"}],"role":"user"}]}}`,
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

func TestGeminiSetupDisablesThinkingOnlyFor25(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-2.5-flash-native-audio-preview-12-2025": true,
		"gemini-3.1-flash-live-preview":                 false,
		"gemini-3.8-live":                               false,
		"gemini-3.8-live-extended-thinking":             false,
		"gemini-9.9-live-not-invented-yet":              false,
		"gemini-2.5-pro-live-preview":                   false,
	} {
		setups := make(chan string, 1)
		var up websocket.Upgrader
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			_, msg, _ := c.ReadMessage()
			setups <- string(msg)
			_ = c.WriteJSON(map[string]any{"setupComplete": map[string]any{}})
			_, _, _ = c.ReadMessage() // hold the socket open until the session closes
		}))
		s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Model: model})
		if err != nil {
			t.Fatal(err)
		}
		var setup struct {
			Setup struct {
				GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
			} `json:"setup"`
		}
		var raw string
		select {
		case raw = <-setups:
		case <-time.After(testTimeout):
			t.Fatalf("%s: timed out waiting for setup", model)
		}
		if err := json.Unmarshal([]byte(raw), &setup); err != nil {
			t.Fatal(err)
		}
		tc, has := setup.Setup.GenerationConfig["thinkingConfig"]
		if want && (!has || string(tc) != `{"thinkingBudget":0}`) {
			t.Fatalf("%s: generationConfig.thinkingConfig = %s, want {\"thinkingBudget\":0}", model, tc)
		}
		// `"thinking` matches a JSON key (thinkingConfig, thinkingLevel, ...) but not a model
		// name that merely ends in "-extended-thinking", where the quote follows the word.
		if !want && (has || strings.Contains(raw, `"thinking`)) {
			t.Fatalf("%s: setup must not carry a thinking config, got %s", model, tc)
		}
		_ = s.Close(context.Background())
		srv.Close()
	}
}

func TestGeminiConnectionLostErrorScrubsKey(t *testing.T) {
	const key = "a+b/c=d"
	s := &Session{Conn: realtimeconn.New(nil)}
	s.SetSecret(key)
	s.onEnd(errors.New("websocket: close 1008: bad key " + key + " / " + url.QueryEscape(key)))
	ev := <-s.Events()
	e, ok := ev.(gptlive.Error)
	if !ok {
		t.Fatalf("first event = %T, want gptlive.Error", ev)
	}
	if msg := e.Err.Error(); strings.Contains(msg, key) || strings.Contains(msg, url.QueryEscape(key)) || !strings.Contains(msg, "***") {
		t.Fatal("connection-lost error must carry the close text with the key masked")
	}
}

// TestGeminiRepeatedUsageMetadataInOneTurnIsCountedOnce pins I3. The pipeline SUMS every
// BackendUsage into the tokens it persists, and a usageMetadata message carries the current
// turn's totals rather than a delta, so a vendor that repeats it within a turn used to
// multiply the persisted tokens by the number of repeats. The totals a live run produces
// today — one usageMetadata per turn, prompt tokens rising per turn, the session total equal
// to the sum of the turn totals — must come out unchanged.
func TestGeminiRepeatedUsageMetadataInOneTurnIsCountedOnce(t *testing.T) {
	usage := func(prompt, response, total int) map[string]any {
		return map[string]any{"usageMetadata": map[string]any{
			"promptTokenCount": prompt, "responseTokenCount": response, "totalTokenCount": total,
		}}
	}
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
		// Turn 1: the same usage repeated, as a vendor sending it per frame would.
		send(usage(9248, 120, 9368))
		send(usage(9248, 120, 9368))
		send(usage(9248, 120, 9368))
		content(map[string]any{"turnComplete": true})
		// Turn 2: its own totals, with the prompt grown by the conversation so far.
		send(usage(10241, 90, 10331))
		content(map[string]any{"turnComplete": true})
		content(map[string]any{"interrupted": true}) // the test's end marker
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	var input, output, total int
	for done := false; !done; {
		switch e := nextEvent(t, s).(type) {
		case gptlive.BackendUsage:
			input, output, total = input+e.Input, output+e.Output, total+e.Total
		case gptlive.Interrupted:
			done = true
		}
	}
	// Exactly the two turns, each counted once: what one usageMetadata per turn already gives.
	if input != 9248+10241 || output != 120+90 || total != 9368+10331 {
		t.Fatalf("summed usage = input %d, output %d, total %d; want %d/%d/%d",
			input, output, total, 9248+10241, 120+90, 9368+10331)
	}
}

// TestGeminiUsageResetsOnATurnThatEndsOnGenerationCompleteAlone pins N2. This file already
// treats a standalone generationComplete as a turn end (it stops the flush timer and
// flushes the user transcript on it), but the per-turn usage baseline used to be cleared
// only on turnComplete/interrupted. A turn that ended on generationComplete alone therefore
// left the previous turn's totals in place, so the NEXT turn's usage was emitted as the
// difference between turns (10241-9248 = 993 instead of 10241) — and because the baseline
// is a running max() the under-count compounded for the rest of the session.
func TestGeminiUsageResetsOnATurnThatEndsOnGenerationCompleteAlone(t *testing.T) {
	usage := func(prompt, response, total int) map[string]any {
		return map[string]any{"usageMetadata": map[string]any{
			"promptTokenCount": prompt, "responseTokenCount": response, "totalTokenCount": total,
		}}
	}
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
		// Turn 1 ends on generationComplete with no turnComplete behind it.
		send(usage(9248, 120, 9368))
		content(map[string]any{"generationComplete": true})
		// Turn 2 must be counted in full, not as its growth over turn 1.
		send(usage(10241, 90, 10331))
		content(map[string]any{"turnComplete": true})
		content(map[string]any{"interrupted": true}) // the test's end marker
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	var input, output, total int
	for done := false; !done; {
		switch e := nextEvent(t, s).(type) {
		case gptlive.BackendUsage:
			input, output, total = input+e.Input, output+e.Output, total+e.Total
		case gptlive.Interrupted:
			done = true
		}
	}
	if input != 9248+10241 || output != 120+90 || total != 9368+10331 {
		t.Fatalf("summed usage = input %d, output %d, total %d; want %d/%d/%d (turn 2 was counted as a delta on turn 1)",
			input, output, total, 9248+10241, 120+90, 9368+10331)
	}
}

// TestGeminiRepeatedUsageMetadataStraddlingATurnEndIsCountedOnce pins B2, the window the
// turn key opened: a usageMetadata repeated within ONE turn but split by a turn-end signal
// used to look like the first usage of a new turn, reset the baseline and be emitted — and,
// since the pipeline sums every BackendUsage, billed — a second time. The dedupe test above
// sends its repeats back to back, so only this shape covers the boundary.
func TestGeminiRepeatedUsageMetadataStraddlingATurnEndIsCountedOnce(t *testing.T) {
	usage := func(prompt, response, total int) map[string]any {
		return map[string]any{"usageMetadata": map[string]any{
			"promptTokenCount": prompt, "responseTokenCount": response, "totalTokenCount": total,
		}}
	}
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
		// One turn, its usage repeated either side of generationComplete.
		content(map[string]any{"modelTurn": map[string]any{"parts": []map[string]any{
			{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte{1, 0, 2, 0})}},
		}}})
		send(usage(9248, 120, 9368))
		content(map[string]any{"generationComplete": true})
		send(usage(9248, 120, 9368))
		content(map[string]any{"turnComplete": true})
		// A second, genuinely new turn still has to be credited in full.
		send(usage(10241, 90, 10331))
		content(map[string]any{"turnComplete": true})
		content(map[string]any{"interrupted": true}) // the test's end marker
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	var input, output, total int
	for done := false; !done; {
		switch e := nextEvent(t, s).(type) {
		case gptlive.BackendUsage:
			input, output, total = input+e.Input, output+e.Output, total+e.Total
		case gptlive.Interrupted:
			done = true
		}
	}
	if input != 9248+10241 || output != 120+90 || total != 9368+10331 {
		t.Fatalf("summed usage = input %d, output %d, total %d; want %d/%d/%d (the repeat across generationComplete was billed again)",
			input, output, total, 9248+10241, 120+90, 9368+10331)
	}
}

// blockingExec holds the tool call open until the test has torn the socket down, so the
// toolResponse is written exactly while the connection is being replaced.
type blockingExec struct{ ready chan struct{} }

func (e blockingExec) Execute(_ context.Context, _ string, _ map[string]any) (string, bool) {
	<-e.ready
	return "scored", false
}

// TestGeminiToolResponseIsReplayedOnTheResumedSocket pins I4. Gemini is the one vendor that
// redials mid-session; a toolResponse written between the read error and the socket swap went
// to a dead socket and was lost, leaving the model waiting on a quiz score that never arrived.
// It must reach the socket that replaces it.
func TestGeminiToolResponseIsReplayedOnTheResumedSocket(t *testing.T) {
	toolDone := make(chan struct{})
	toolResponses := make(chan string, 4)
	var conns atomic.Int32
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.ReadMessage() // setup
		send := func(v any) { _ = c.WriteMessage(websocket.BinaryMessage, []byte(mustJSON(v))) }
		send(map[string]any{"setupComplete": map[string]any{}})
		if conns.Add(1) == 1 {
			send(map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": "h1", "resumable": true}})
			send(map[string]any{"toolCall": map[string]any{"functionCalls": []any{
				map[string]any{"id": "f1", "name": "quiz_score_answer", "args": map[string]any{"correct": true}},
			}}})
			time.Sleep(50 * time.Millisecond) // let the toolCall land, then drop the socket
			_ = c.Close()
			close(toolDone) // only now does the tool return, so its write hits the dead socket
			return
		}
		// The resumed socket keeps producing frames, so a result queued a moment after the
		// resume finished is flushed too.
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for i := 0; i < 40; i++ {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
				send(map[string]any{"serverContent": map[string]any{"outputTranscription": map[string]any{"text": "."}}})
			}
		}()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if strings.Contains(string(data), "toolResponse") {
				toolResponses <- strings.TrimSpace(string(data)) // WriteJSON appends a newline
			}
		}
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), Config{APIKey: "g", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
		Executor: blockingExec{ready: toolDone}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	select {
	case got := <-toolResponses:
		want := `{"toolResponse":{"functionResponses":[{"id":"f1","name":"quiz_score_answer","response":{"output":"scored"}}]}}`
		if got != want {
			t.Fatalf("replayed toolResponse = %s, want %s", got, want)
		}
	case <-time.After(testTimeout):
		t.Fatal("the toolResponse was lost with the socket it was written to; the quiz stalls here")
	}
}
