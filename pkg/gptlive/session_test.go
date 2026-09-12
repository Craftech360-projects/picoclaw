package gptlive

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func testConfig(url string) Config {
	return Config{APIKey: "sk-test", BaseURL: url, Model: DefaultModel, Voice: "marin", Instructions: "test",
		SampleRate: 16000, Backend: ResponsesConfig{Model: DefaultBackendModel}, DialTimeout: 2 * time.Second, RetryInterval: 10 * time.Millisecond}
}

func TestDialSendsSessionStartAndWaitsForStarted(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) { acceptStart(f)(c, ev) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if s.ID() != "live_test" {
		t.Errorf("session id %q", s.ID())
	}
	starts := f.received(EventSessionStart)
	if len(starts) != 1 {
		t.Fatalf("want one session.start, got %d", len(starts))
	}
	sess := starts[0]["session"].(map[string]any)
	if sess["instructions"] != "test" || sess["model"] != DefaultModel {
		t.Errorf("session config not sent: %v", sess)
	}
	audio := sess["audio"].(map[string]any)["format"].(map[string]any)
	if audio["rate"].(float64) != 16000 {
		t.Errorf("rate not sent: %v", audio)
	}
	select {
	case ev := <-s.Events():
		if _, ok := ev.(SessionStarted); !ok {
			t.Errorf("first event should be SessionStarted, got %T", ev)
		}
	case <-time.After(time.Second):
		t.Error("no SessionStarted event")
	}
}

func TestNothingIsSentBeforeStarted(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			time.Sleep(150 * time.Millisecond) // the client must hold audio until session.started
			acceptStart(f)(c, ev)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	s.PushAudio(make([]byte, 640))
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	order := make([]string, 0, len(f.events))
	for _, ev := range f.events {
		order = append(order, ev["type"].(string))
	}
	f.mu.Unlock()
	if len(order) < 2 || order[0] != EventSessionStart || order[1] != EventInputAudioAppend {
		t.Errorf("audio must follow session.start and wait for started; order %v", order)
	}
}

func TestCloseSendsSessionCloseAndWaitsForClosed(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) { acceptStart(f)(c, ev) })
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.received(EventSessionClose)); n != 1 {
		t.Errorf("want one session.close, got %d", n)
	}
	var sawClosed bool
	for ev := range s.Events() {
		if c, ok := ev.(Closed); ok && c.Reason == "close_requested" && c.VoiceSeconds == 3 {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Error("Closed event with final usage not delivered")
	}
}

// TestFatalProtocolErrorEmitsOnce covers Finding 2 from the Task 3 review: a fatal
// EventError must produce exactly one Error event, and that event's error must expose
// the fatal ErrorBody so a retry loop can decide not to reconnect.
func TestFatalProtocolErrorEmitsOnce(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "fatal_test"}})
			f.send(c, map[string]any{"type": EventError, "error": map[string]any{
				"type": "invalid_request_error", "code": "invalid_api_key", "message": "bad key",
			}})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	// The session already ended on its own (fatal error); tear down its context
	// directly instead of Close(), which would otherwise wait the full
	// sessionCloseTimeout for a session.closed reply that will never arrive.
	defer s.cancel()

	var errs []Error
	timeout := time.After(2 * time.Second)
drain:
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				break drain
			}
			if e, ok := ev.(Error); ok {
				errs = append(errs, e)
			}
		case <-timeout:
			t.Fatal("timed out waiting for events channel to close")
		}
	}
	if len(errs) != 1 {
		t.Fatalf("want exactly one Error event, got %d: %v", len(errs), errs)
	}
	var fp *FatalProtocolError
	if !errors.As(errs[0].Err, &fp) {
		t.Fatalf("Error.Err does not expose a *FatalProtocolError: %v", errs[0].Err)
	}
	if fp.Body.Code != "invalid_api_key" {
		t.Errorf("fatal code = %q, want invalid_api_key", fp.Body.Code)
	}
	if errs[0].Recoverable {
		t.Error("fatal error should be marked non-recoverable")
	}
}

// TestCloseJoinsReadGoroutineBeforeClosingChannels covers Finding 1 from the Task 3
// review: runOnce must not return (letting run() close s.events/s.audio) while the
// read goroutine could still be inside handleEvent sending on those channels, which
// would panic since a send on an already-closed channel is always ready to proceed.
//
// The fake server floods far more output_audio deltas than s.audio's buffer holds,
// and the test never drains Audio(), so the read goroutine blocks inside handleEvent's
// audio select once the buffer fills. session.close cannot be acked by this session's
// own read goroutine while it is stuck (the reply is queued behind the flood), so
// Close falls back to sessionCloseTimeout before cancelling — exactly the moment the
// stuck goroutine has a large backlog still to race through against run() closing the
// channels. A panic here crashes the whole test binary, which fails the run.
func TestCloseJoinsReadGoroutineBeforeClosingChannels(t *testing.T) {
	var f *fakeLive
	stopFlood := make(chan struct{})
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "flood_test"}})
			payload := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4})
			go func() {
				for i := 0; i < 20000; i++ {
					select {
					case <-stopFlood:
						return
					default:
						f.send(c, map[string]any{"type": EventOutputAudioDelta, "delta": payload})
					}
				}
			}()
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested", "usage": map[string]any{"seconds": 1}})
		}
	})
	defer close(stopFlood)

	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately do not drain s.Audio(): once its buffer (1024) fills, the read
	// goroutine blocks mid-handleEvent, which is exactly the state Finding 1 says
	// must not race with run() closing the channels.
	time.Sleep(150 * time.Millisecond) // let the flood fill the audio buffer

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Close did not return in time")
	}

	// If run() closed s.audio/s.events while the read goroutine was still sending on
	// them, that send panics and crashes the test binary before reaching this point.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range s.Audio() {
		}
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("s.Audio() never closed after Close returned")
	}
}

// TestCloseReturnsImmediatelyWhenTheRunLoopAlreadyExited covers Minor 7 of the
// final whole-branch review. On the unrecoverable-error path (MaxRetries
// exhausted, or a fatal protocol error) run() has already returned and closed
// s.done, so nothing is left reading s.out. Close still sent session.close into
// that queue and then waited the full sessionCloseTimeout — five seconds — for
// a session.closed that could never arrive, on every single failed session.
//
// The fake server here answers session.start and then reports a FATAL protocol
// error, which is precisely that path: run emits one non-recoverable Error and
// returns. Close must then notice the run loop is gone and return promptly.
func TestCloseReturnsImmediatelyWhenTheRunLoopAlreadyExited(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "dead_test"}})
			f.send(c, map[string]any{"type": EventError, "error": map[string]any{
				"type": "invalid_request_error", "code": "invalid_api_key", "message": "bad key",
			}})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the run loop to actually exit, which is the state this is about.
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("run loop never exited after the fatal protocol error")
	}

	start := time.Now()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// sessionCloseTimeout is 5s; anything near it means Close waited for a
	// session.closed reply that nothing could ever send.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close took %v after the run loop had already exited; it must not wait out "+
			"sessionCloseTimeout (%v) for a reply that can never arrive", elapsed, sessionCloseTimeout)
	}
	if got := len(f.received(EventSessionClose)); got != 0 {
		t.Errorf("Close sent %d session.close events into a queue nobody reads; want 0", got)
	}
}
