// pkg/grokvoice/session_test.go
package grokvoice

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/realtimeconn"
)

// testTimeout bounds every blocking receive in these tests so a regression
// fails fast instead of hanging to the 10-minute go test default.
const testTimeout = 5 * time.Second

// recvMap waits for one client->server message, failing the test (instead of
// hanging forever) if none arrives within testTimeout.
func recvMap(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a client message")
		return nil
	}
}

func recvAudio(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for audio")
		return nil
	}
}

// collectEvents drains ch until it closes, failing the test instead of
// hanging forever if that takes longer than testTimeout.
func collectEvents(t *testing.T, ch <-chan gptlive.Event) []gptlive.Event {
	t.Helper()
	var events []gptlive.Event
	deadline := time.After(testTimeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("timed out collecting events; got %d so far: %#v", len(events), events)
			return events
		}
	}
}

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
			if err := c.ReadJSON(&m); err != nil {
				t.Errorf("server ReadJSON: %v", err)
				return nil
			}
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
		// response.create is only sent once response.done arrives and every
		// pending call has returned, so response.done must be written before the
		// client will send it (see maybeContinue in session.go).
		_ = c.WriteJSON(map[string]any{"type": "response.done", "response": map[string]any{"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}})
		read()                             // response.create
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

	update := recvMap(t, fromClient)
	session, _ := update["session"].(map[string]any)
	vad, _ := session["turn_detection"].(map[string]any)
	tools, _ := session["tools"].([]any)
	if update["type"] != "session.update" || session["voice"] != "ara" || session["instructions"] != "be kind" ||
		vad["silence_duration_ms"] != float64(700) || len(tools) != 2 {
		t.Fatalf("session.update = %v", update)
	}
	if got := recvAudio(t, s.Audio()); string(got) != string(pcm) {
		t.Fatalf("audio = %v", got)
	}
	out := recvMap(t, fromClient)
	item, _ := out["item"].(map[string]any)
	if out["type"] != "conversation.item.create" || item["output"] != "ran get_time" {
		t.Fatalf("tool output = %v", out)
	}
	out = recvMap(t, fromClient)
	if out["type"] != "response.create" {
		t.Fatalf("after tool output = %v", out)
	}

	events := collectEvents(t, s.Events())
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

// TestGrokTwoToolCallsYieldOneResponseCreateAfterDone covers code-review finding 1:
// when a response makes two tool calls, exactly one response.create must be sent,
// and only after response.done has arrived and both tool outputs have gone out —
// never one per call, and never before the response is done.
func TestGrokTwoToolCallsYieldOneResponseCreateAfterDone(t *testing.T) {
	// gorilla/websocket caches a Conn's first read error (including a read
	// deadline expiring) and returns it from every later read too, so
	// confirming "no message yet" can't be done by giving one blocking
	// ReadJSON a short deadline and continuing to read the same Conn
	// afterward. Instead a single goroutine reads continuously for the life
	// of the connection and hands decoded messages to fromClient; every
	// check below (including the "nothing arrived" ones) is a timed select
	// on that channel.
	fromClient := make(chan map[string]any, 16)
	done := make(chan struct{})
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		go func() {
			defer close(fromClient)
			for {
				var m map[string]any
				if c.ReadJSON(&m) != nil {
					return
				}
				fromClient <- m
			}
		}()
		recv := func() map[string]any {
			select {
			case m, ok := <-fromClient:
				if !ok {
					t.Errorf("server: client connection closed early")
				}
				return m
			case <-time.After(testTimeout):
				t.Errorf("server: timed out waiting for a client message")
				return nil
			}
		}
		expectNone := func(d time.Duration) {
			select {
			case m, ok := <-fromClient:
				if ok {
					t.Errorf("unexpected client message: %v", m)
				}
			case <-time.After(d):
			}
		}

		if recv() == nil { // session.update
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "response.function_call_arguments.done", "call_id": "c1", "name": "get_time", "arguments": "{}"})
		_ = c.WriteJSON(map[string]any{"type": "response.function_call_arguments.done", "call_id": "c2", "name": "get_weather", "arguments": "{}"})

		// Both tool outputs must arrive; the two tool calls run on their own
		// goroutines (conn.Go), so their arrival order is not guaranteed.
		seen := map[string]bool{}
		for len(seen) < 2 {
			m := recv()
			if m == nil {
				return
			}
			if m["type"] != "conversation.item.create" {
				t.Errorf("expected a tool output, got %v", m)
				return
			}
			item, _ := m["item"].(map[string]any)
			callID, _ := item["call_id"].(string)
			seen[callID] = true
		}
		if !seen["c1"] || !seen["c2"] {
			t.Errorf("expected outputs for both calls, got %v", seen)
		}

		// response.done has not arrived yet: no response.create should show up.
		expectNone(150 * time.Millisecond)

		_ = c.WriteJSON(map[string]any{"type": "response.done", "response": map[string]any{"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})

		m := recv() // the single response.create
		if m == nil {
			return
		}
		if m["type"] != "response.create" {
			t.Errorf("expected response.create, got %v", m)
		}

		// No duplicate response.create follows.
		expectNone(150 * time.Millisecond)
	}))
	defer srv.Close()

	s, err := Dial(context.Background(), Config{
		APIKey: "k", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Executor: exec{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for the fake server to finish")
	}
}

// interruptExec blocks a named call on release, so a test can control exactly
// when a "slow" tool finishes relative to other server-sent events, while any
// other call answers immediately.
type interruptExec struct {
	slowName string
	release  chan struct{}
}

func (e interruptExec) Execute(_ context.Context, name string, _ map[string]any) (string, bool) {
	if name == e.slowName {
		<-e.release
	}
	return "ran " + name, false
}

// TestGrokSlowToolAnsweredDuringInterruptingResponseDefersCreate covers the
// round-2 review finding: response A calls a slow tool (c1); A's response.done
// arrives before the tool returns; the child interrupts and the server starts
// response B while c1 is still running. c1's output must still reach the
// server, but the response.create that resumes the conversation must wait for
// B's own response.done — sending (and having it rejected) while B is active,
// or losing track of c1 once B's own bookkeeping overwrote it, was the bug a
// single shared, unkeyed continuation state produced.
//
// The test is deterministic (no reliance on wall-clock timing to order the
// slow tool's completion against the network): before releasing c1, it first
// waits to receive the tool output for a second, fast call (c-sync) that is
// only registered after response B's response.created. Because a single
// reader goroutine processes frames on one connection strictly in the order
// they were written, seeing c-sync's output proves the client has already
// processed response.created for B — and so has already latched "a response
// is active" — before c1 is ever released.
func TestGrokSlowToolAnsweredDuringInterruptingResponseDefersCreate(t *testing.T) {
	fromClient := make(chan map[string]any, 16)
	release := make(chan struct{})
	done := make(chan struct{})
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		go func() {
			defer close(fromClient)
			for {
				var m map[string]any
				if c.ReadJSON(&m) != nil {
					return
				}
				fromClient <- m
			}
		}()
		recv := func() map[string]any {
			select {
			case m, ok := <-fromClient:
				if !ok {
					t.Errorf("server: client connection closed early")
				}
				return m
			case <-time.After(testTimeout):
				t.Errorf("server: timed out waiting for a client message")
				return nil
			}
		}
		expectNone := func(d time.Duration) {
			select {
			case m, ok := <-fromClient:
				if ok {
					t.Errorf("unexpected client message: %v", m)
				}
			case <-time.After(d):
			}
		}
		toolOutputCallID := func(m map[string]any) (string, bool) {
			if m == nil || m["type"] != "conversation.item.create" {
				return "", false
			}
			item, _ := m["item"].(map[string]any)
			id, _ := item["call_id"].(string)
			return id, id != ""
		}

		if recv() == nil { // session.update
			return
		}

		_ = c.WriteJSON(map[string]any{"type": "response.created"}) // response A starts
		_ = c.WriteJSON(map[string]any{"type": "response.function_call_arguments.done", "call_id": "c1", "name": "slow_tool", "arguments": "{}"})
		_ = c.WriteJSON(map[string]any{"type": "response.done"}) // A finishes; c1 (slow_tool) is still outstanding

		_ = c.WriteJSON(map[string]any{"type": "response.created"}) // the child interrupts; response B starts
		_ = c.WriteJSON(map[string]any{"type": "response.function_call_arguments.done", "call_id": "c-sync", "name": "fast_tool", "arguments": "{}"})

		if id, ok := toolOutputCallID(recv()); !ok || id != "c-sync" {
			t.Errorf("expected c-sync's tool output first (proves response.created for B was already processed), got call_id=%q ok=%v", id, ok)
			return
		}

		close(release) // only now is it safe to let the slow tool (c1) finish

		if id, ok := toolOutputCallID(recv()); !ok || id != "c1" {
			t.Errorf("expected c1's tool output, got call_id=%q ok=%v", id, ok)
			return
		}

		// response.create must be withheld here: B is still active. The old
		// shared, unkeyed state would have sent one right now and had it
		// rejected by the protocol.
		expectNone(150 * time.Millisecond)

		_ = c.WriteJSON(map[string]any{"type": "response.done"}) // B finishes

		m := recv() // the single, deferred response.create
		if m == nil {
			return
		}
		if m["type"] != "response.create" {
			t.Errorf("expected response.create, got %v", m)
		}
		expectNone(150 * time.Millisecond)
	}))
	defer srv.Close()

	s, err := Dial(context.Background(), Config{
		APIKey: "k", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
		Executor: interruptExec{slowName: "slow_tool", release: release},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for the fake server to finish")
	}
}

// TestGrokAgentTextResetsAcrossResponses covers code-review finding 2: agentText
// must not accumulate across responses, including when item_id is absent and every
// delta would otherwise land under the same "" key for the life of the session.
func TestGrokAgentTextResetsAcrossResponses(t *testing.T) {
	fromClient := make(chan map[string]any, 8)
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil { // session.update
			return
		}
		fromClient <- m
		_ = c.WriteJSON(map[string]any{"type": "response.output_audio_transcript.delta", "delta": "first response"})
		_ = c.WriteJSON(map[string]any{"type": "response.done"})
		_ = c.WriteJSON(map[string]any{"type": "response.output_audio_transcript.delta", "delta": "second response"})
		_ = c.WriteJSON(map[string]any{"type": "response.done"})
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	s, err := Dial(context.Background(), Config{APIKey: "k", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}

	recvMap(t, fromClient) // session.update

	var transcripts []gptlive.AgentTranscript
	deadline := time.After(testTimeout)
	for len(transcripts) < 2 {
		select {
		case ev := <-s.Events():
			if at, ok := ev.(gptlive.AgentTranscript); ok {
				transcripts = append(transcripts, at)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for agent transcripts; got %v", transcripts)
		}
	}
	if transcripts[0].Text != "first response" {
		t.Fatalf("first response text = %q, want %q", transcripts[0].Text, "first response")
	}
	if transcripts[1].Text != "second response" {
		t.Fatalf("second response text = %q, want %q (agentText leaked across responses)", transcripts[1].Text, "second response")
	}
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
	recvMap(t, fromClient) // initial session.update
	s.AppendCommentary("Greet the child now.")
	s.AppendInstructions("Ask question 2 next.")
	if m := recvMap(t, fromClient); m["type"] != "conversation.item.create" {
		t.Fatalf("commentary item = %v", m)
	}
	if m := recvMap(t, fromClient); m["type"] != "response.create" {
		t.Fatalf("commentary response = %v", m)
	}
	m := recvMap(t, fromClient)
	session, _ := m["session"].(map[string]any)
	if m["type"] != "session.update" || session["instructions"] != "base\n\nAsk question 2 next." {
		t.Fatalf("instructions update = %v", m)
	}
}

func TestGrokConnectionLostErrorScrubsKey(t *testing.T) {
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

// doneCapture is a replacement for the package's logDone sink that records what
// the response.done diagnostic emitted. Dial copies logDone into the Session, so
// installing one before Dial and restoring it after the session ends never races
// a session that is still reading frames.
type doneCapture struct {
	mu   sync.Mutex
	logs []map[string]any
}

func (c *doneCapture) install(t *testing.T) {
	t.Helper()
	prev := logDone
	logDone = func(fields map[string]any) {
		c.mu.Lock()
		c.logs = append(c.logs, fields)
		c.mu.Unlock()
	}
	t.Cleanup(func() { logDone = prev })
}

func (c *doneCapture) all() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.logs...)
}

// runDone runs one fake-server session that sends a single response.done frame
// verbatim (a raw string, so the test can feed the vendor's documented JSON byte
// for byte rather than a Go map's re-encoding of it) and returns every event the
// session emitted plus everything the diagnostic logged.
func runDone(t *testing.T, doneFrame string) ([]gptlive.Event, []map[string]any) {
	t.Helper()
	var capture doneCapture
	capture.install(t)

	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var m map[string]any
		if c.ReadJSON(&m) != nil { // session.update
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(doneFrame))
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	// A realistic key, not the single letter the other tests use: Conn.Scrub
	// masks every occurrence of the secret in what it is handed, and a one-letter
	// secret would punch holes in the shape dump's own field names.
	s, err := Dial(context.Background(), Config{APIKey: "xai-test-key-0123", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}

	// VoiceUsage is emitted for every response.done, after the BackendUsage (if
	// any), so seeing it means the frame has been fully handled and the
	// diagnostic has already run.
	var events []gptlive.Event
	deadline := time.After(testTimeout)
	for done := false; !done; {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before response.done was handled")
			}
			events = append(events, ev)
			_, done = ev.(gptlive.VoiceUsage)
		case <-deadline:
			t.Fatalf("timed out waiting for response.done to be handled; got %#v", events)
		}
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return events, capture.all()
}

// usageFromDone is runDone reduced to the BackendUsage the session emitted, plus
// the one diagnostic line the frame produced.
func usageFromDone(t *testing.T, doneFrame string) (gptlive.BackendUsage, map[string]any) {
	t.Helper()
	events, logs := runDone(t, doneFrame)
	if len(logs) != 1 {
		t.Fatalf("diagnostic lines = %d, want exactly 1: %#v", len(logs), logs)
	}
	for _, ev := range events {
		if u, ok := ev.(gptlive.BackendUsage); ok {
			return u, logs[0]
		}
	}
	t.Fatalf("no BackendUsage among %#v", events)
	return gptlive.BackendUsage{}, nil
}

// TestGrokResponseDoneUsageShapes pins the mapping of
// response.done.response.usage onto gptlive.BackendUsage for every spelling xAI
// uses for that object, and the *_from labels that say which spelling a live
// frame actually used. The first case is the shape xAI's own websocket schema
// documents (https://docs.x.ai/voice-realtime.ws.json); the rest are the
// spellings its REST spec (https://docs.x.ai/openapi.json: Usage, ModelUsage)
// and the OpenAI realtime event it is modelled on use for the same numbers —
// which is what a live run turned out to be sending, producing has_usage=true
// and a zero-token session.
//
// Every case's numbers are deliberately distinguishable: no flat number equals
// the sum of the breakdown beside it, and no vendor-sent total_tokens equals
// input+output, so dropping any one json tag — or silently recomputing a total
// the vendor did send — fails a case instead of passing by arithmetic accident.
func TestGrokResponseDoneUsageShapes(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		frame                        string
		input, output, total, cached int
		reasoning                    int
		inFrom, outFrom, totalFrom   string
	}{
		{
			// Exactly xAI's documented response.done, including the surrounding
			// response object fields from the schema's own example. total_tokens
			// is NOT input+output: a vendor-reported total must be passed through.
			name: "xai voice-realtime schema, flat integers",
			frame: `{"event_id":"event_3132","type":"response.done","response":{"id":"resp_001","object":"realtime.response","status":"completed",
				"usage":{"input_tokens":120,"output_tokens":48,"total_tokens":171}}}`,
			input: 120, output: 48, total: 171,
			inFrom: "input_tokens", outFrom: "output_tokens", totalFrom: "total_tokens",
		},
		{
			// xAI REST "Usage": prompt_/completion_ naming plus its detail objects.
			// The breakdowns sum to 125 and 48 — neither matches the flat number
			// beside it, so this case only passes if the flat member wins.
			name: "xai chat-completions spelling",
			frame: `{"type":"response.done","response":{"status":"completed","usage":{
				"prompt_tokens":111,"completion_tokens":47,"total_tokens":158,
				"prompt_tokens_details":{"text_tokens":30,"audio_tokens":90,"image_tokens":5,"cached_tokens":64},
				"completion_tokens_details":{"reasoning_tokens":8,"audio_tokens":40,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0},
				"num_sources_used":0}}}`,
			input: 111, output: 47, total: 158, cached: 64, reasoning: 8,
			inFrom: "prompt_tokens", outFrom: "completion_tokens", totalFrom: "total_tokens",
		},
		{
			// The same spelling with no detail objects at all — the frame that
			// regresses to tokens=0 the moment the prompt_tokens/completion_tokens
			// tags are dropped, which no details-carrying case can catch.
			name: "xai chat-completions spelling, no details",
			frame: `{"type":"response.done","response":{"status":"completed","usage":{
				"prompt_tokens":77,"completion_tokens":33,"total_tokens":110}}}`,
			input: 77, output: 33, total: 110,
			inFrom: "prompt_tokens", outFrom: "completion_tokens", totalFrom: "total_tokens",
		},
		{
			// OpenAI realtime's response.done usage with only the nested
			// breakdowns filled in: the totals have to be summed out of them,
			// leaving cached_tokens out (it is a subset of text_tokens), and
			// total_tokens has to be derived because the vendor sent none.
			name: "nested token details only, no flat totals",
			frame: `{"type":"response.done","response":{"status":"completed","usage":{
				"input_token_details":{"text_tokens":31,"audio_tokens":90,"cached_tokens":64},
				"output_token_details":{"text_tokens":9,"audio_tokens":40}}}}`,
			input: 121, output: 49, total: 170, cached: 64,
			inFrom: "input_token_details sum", outFrom: "output_token_details sum", totalFrom: "input+output",
		},
		{
			// xAI REST "ModelUsage" spelling, flat numbers present but zero.
			// Image and reasoning tokens count towards their side's total: xAI's
			// MediaUsage documents output_tokens as text + reasoning + image.
			name: "responses-style details with zeroed flat totals",
			frame: `{"type":"response.done","response":{"status":"completed","usage":{
				"input_tokens":0,"output_tokens":0,"total_tokens":0,
				"input_tokens_details":{"text_tokens":30,"audio_tokens":90,"image_tokens":5,"cached_tokens":64},
				"output_tokens_details":{"text_tokens":40,"reasoning_tokens":8,"image_tokens":2}}}}`,
			input: 125, output: 50, total: 175, cached: 64, reasoning: 8,
			inFrom: "input_tokens_details sum", outFrom: "output_tokens_details sum", totalFrom: "input+output",
		},
		{
			// Nothing recognizable: the usage object is still reported (as
			// zeros), which is the case that makes handle log the frame's shape.
			name:   "unrecognized usage object maps to zeros",
			frame:  `{"type":"response.done","response":{"status":"completed","usage":{"tokens_used":168}}}`,
			inFrom: "none", outFrom: "none", totalFrom: "input+output",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, line := usageFromDone(t, tc.frame)
			if got.Input != tc.input || got.Output != tc.output || got.Total != tc.total {
				t.Fatalf("input/output/total = %d/%d/%d, want %d/%d/%d (%#v)",
					got.Input, got.Output, got.Total, tc.input, tc.output, tc.total, got)
			}
			if got.Cached != tc.cached || got.Reasoning != tc.reasoning {
				t.Fatalf("cached/reasoning = %d/%d, want %d/%d", got.Cached, got.Reasoning, tc.cached, tc.reasoning)
			}
			if got.Model != DefaultModel {
				t.Fatalf("model = %q, want %q", got.Model, DefaultModel)
			}
			// The line must always say which spelling produced the numbers, not
			// only when there are none.
			for field, want := range map[string]any{
				"input": tc.input, "output": tc.output, "total": tc.total,
				"input_from": tc.inFrom, "output_from": tc.outFrom, "total_from": tc.totalFrom,
				"has_usage": true,
			} {
				if line[field] != want {
					t.Fatalf("log %s = %v, want %v (line %v)", field, line[field], want, line)
				}
			}
		})
	}
}

// TestGrokResponseDoneWithoutTokensLogsTheShape drives the diagnostic itself
// through a real session: a usage-less frame and an all-zero-usage frame must
// BOTH produce a shape dump (the live bug reported has_usage=true with zero
// tokens and printed nothing that could explain it), and neither may carry a
// string value out of the frame — this API's response.done can hold
// response.output[].content[].transcript.
func TestGrokResponseDoneWithoutTokensLogsTheShape(t *testing.T) {
	const said = "tell me the one about the elephant"
	for _, tc := range []struct {
		name      string
		frame     string
		hasUsage  bool
		wantShape []string
	}{
		{
			name: "no usage object at all",
			frame: `{"type":"response.done","response":{"status":"completed",
				"output":[{"type":"message","content":[{"type":"audio","transcript":"` + said + `"}]}]}}`,
			wantShape: []string{"output:[1x", "transcript:str", "status:completed"},
		},
		{
			name: "usage present but every number zero",
			frame: `{"type":"response.done","response":{"status":"completed",
				"output":[{"type":"message","content":[{"type":"audio","transcript":"` + said + `"}]}],
				"usage":{"cost_in_usd_ticks":420,"num_sources_used":0}}}`,
			hasUsage:  true,
			wantShape: []string{"cost_in_usd_ticks:420", "num_sources_used:0", "transcript:str"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, logs := runDone(t, tc.frame)
			if len(logs) != 1 {
				t.Fatalf("diagnostic lines = %d, want exactly 1: %#v", len(logs), logs)
			}
			line := logs[0]
			if line["has_usage"] != tc.hasUsage {
				t.Fatalf("has_usage = %v, want %v", line["has_usage"], tc.hasUsage)
			}
			if line["input"] != 0 || line["output"] != 0 || line["total"] != 0 {
				t.Fatalf("expected zeroed numbers, got %v", line)
			}
			shape, ok := line["shape"].(string)
			if !ok {
				t.Fatalf("no shape dump on a frame that parsed to nothing: %v", line)
			}
			for _, want := range tc.wantShape {
				if !strings.Contains(shape, want) {
					t.Fatalf("shape = %q, want it to contain %q", shape, want)
				}
			}
			for _, leak := range []string{said, "elephant"} {
				if strings.Contains(shape, leak) {
					t.Fatalf("shape leaked frame text %q: %q", leak, shape)
				}
			}
			if !tc.hasUsage {
				for _, ev := range events {
					if u, ok := ev.(gptlive.BackendUsage); ok {
						t.Fatalf("usage-less frame emitted %#v", u)
					}
				}
			}
		})
	}
}

// TestDescribeJSONNamesFieldsWithoutText covers the diagnostic's formatter
// directly: it must name every field of a frame we failed to map — that is the
// whole point of logging it — while never letting a transcript through.
func TestDescribeJSONNamesFieldsWithoutText(t *testing.T) {
	const secret = "the child said her name is Priya"
	frame := `{"type":"response.done","response":{"id":"resp_1","object":"realtime.response","status":"completed",
		"output":[{"type":"message","content":[{"type":"audio","transcript":"` + secret + `"}]}],
		"usage":{"tokens_used":168,"prompt_tokens_details":{"audio_tokens":90}}}}`

	got := describeJSON([]byte(frame))
	for _, want := range []string{"tokens_used:168", "prompt_tokens_details", "audio_tokens:90", "status:completed", "transcript:str"} {
		if !strings.Contains(got, want) {
			t.Fatalf("describeJSON = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "Priya") || strings.Contains(got, secret) {
		t.Fatalf("describeJSON leaked transcript text: %q", got)
	}
	if got := describeJSON([]byte("not json")); got != "unparsed" {
		t.Fatalf("describeJSON(non-JSON) = %q, want %q", got, "unparsed")
	}
}

// TestClipCutsOnRuneBoundaries: the shape dump is scrubbed first and clipped
// second, and a cut through a multi-byte rune would emit replacement garbage.
func TestClipCutsOnRuneBoundaries(t *testing.T) {
	if got := clip("abc", 10); got != "abc" {
		t.Fatalf("clip(short) = %q, want it untouched", got)
	}
	got := clip("héllo", 2) // 6 bytes: a limit of 2 falls inside the é
	if got != "h..." {
		t.Fatalf("clip = %q, want %q", got, "h...")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clip produced invalid UTF-8: %q", got)
	}
}
