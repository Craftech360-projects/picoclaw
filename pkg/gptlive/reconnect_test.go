package gptlive

import (
	"context"
	"testing"
	"time"
)

func TestReconnectReplaysHistoryAsStartupInput(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			n := len(f.received(EventSessionStart))
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_" + string(rune('0'+n))}})
			if n == 1 {
				f.send(c, map[string]any{"type": EventInputTranscriptDelta, "delta": "hello cheeko", "start_ms": 0, "end_ms": 500})
				time.Sleep(50 * time.Millisecond)
				_ = c.Close() // drop the first connection without session.closed
			}
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested"})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.received(EventSessionStart)) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	starts := f.received(EventSessionStart)
	if len(starts) != 2 {
		t.Fatalf("expected a reconnect, got %d session.start", len(starts))
	}
	input, _ := starts[1]["session"].(map[string]any)["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("second start must carry the transcript as history: %v", starts[1])
	}
	msg := input[0].(map[string]any)
	if msg["role"] != "user" || msg["content"].([]any)[0].(map[string]any)["text"] != "hello cheeko" {
		t.Errorf("history item wrong: %v", msg)
	}
	var sawReconnect bool
	timeout := time.After(2 * time.Second)
	for !sawReconnect {
		select {
		case ev := <-s.Events():
			if _, ok := ev.(Reconnected); ok {
				sawReconnect = true
			}
		case <-timeout:
			t.Fatal("no Reconnected event")
		}
	}
}

func TestFatalErrorDoesNotReconnect(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			f.send(c, map[string]any{"type": EventError, "error": map[string]any{"type": "invalid_request_error", "code": "forbidden", "message": "Voice session access denied."}})
		}
	})
	_, err := Dial(context.Background(), testConfig(f.url()))
	if err == nil {
		t.Fatal("dial must fail on a fatal error")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.received(EventSessionStart)); n != 1 {
		t.Errorf("fatal error must not retry, got %d starts", n)
	}
}
