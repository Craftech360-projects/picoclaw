package gptlive

import (
	"context"
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
