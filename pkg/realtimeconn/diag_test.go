package realtimeconn

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestLogLimiterAllowsOncePerIntervalPerKeyAndCountsHeld(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLogLimiter(10 * time.Second)
	l.now = func() time.Time { return now }

	if ok, held := l.Allow("a"); !ok || held != 0 {
		t.Fatalf("first a: ok=%v held=%d, want true 0", ok, held)
	}
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); ok {
			t.Fatal("a within the interval must be held back")
		}
	}
	if ok, held := l.Allow("b"); !ok || held != 0 {
		t.Fatalf("keys are independent: b ok=%v held=%d", ok, held)
	}
	now = now.Add(10 * time.Second)
	if ok, held := l.Allow("a"); !ok || held != 3 {
		t.Fatalf("a after the interval: ok=%v held=%d, want true 3", ok, held)
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("the held count resets and the interval restarts")
	}
}

func TestLogLimiterNilAllowsEverythingAndKeysAreBounded(t *testing.T) {
	var nl *LogLimiter
	if ok, _ := nl.Allow("x"); !ok {
		t.Fatal("a nil limiter must not suppress logs")
	}
	l := NewLogLimiter(time.Hour)
	for i := 0; i < maxLimiterKeys+10; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if len(l.last) > maxLimiterKeys+1 {
		t.Fatalf("limiter tracks %d keys, want at most %d", len(l.last), maxLimiterKeys+1)
	}
	if ok, _ := l.Allow("another-new-key"); ok {
		t.Fatal("once the key budget is spent, new keys share the overflow key's interval")
	}
}

func TestCloseDetails(t *testing.T) {
	code, text := closeDetails(fmt.Errorf("wrapped: %w", &websocket.CloseError{Code: 1011, Text: "Deadline expired"}))
	if code != 1011 || text != "Deadline expired" {
		t.Errorf("close frame: code=%d text=%q", code, text)
	}
	code, text = closeDetails(errors.New("read tcp 10.0.0.1:1->10.0.0.2:443: wsarecv: connection reset"))
	if code != 0 || !strings.Contains(text, "connection reset") {
		t.Errorf("no close frame: code=%d text=%q", code, text)
	}
	if code, text = closeDetails(nil); code != 0 || text != "" {
		t.Errorf("nil: code=%d text=%q", code, text)
	}
	_, text = closeDetails(errors.New(strings.Repeat("x", 1000)))
	if len(text) > maxLogText+3 {
		t.Errorf("text not bounded: %d bytes", len(text))
	}
}
