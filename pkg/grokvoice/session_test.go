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
	"testing"
	"time"

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
