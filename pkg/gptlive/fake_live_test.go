package gptlive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

type websocketConn = websocket.Conn

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }

// fakeLive is a scripted GPT-Live server. handle receives every client event and
// may reply; it runs on the read goroutine, so replies are ordered after the event.
type fakeLive struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	conns   []*websocket.Conn
	handle  func(conn *websocket.Conn, ev map[string]any)
	events  []map[string]any
	upgrade websocket.Upgrader
}

func newFakeLive(t *testing.T, handle func(conn *websocket.Conn, ev map[string]any)) *fakeLive {
	t.Helper()
	f := &fakeLive{t: t, handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		conn, err := f.upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				f.t.Errorf("client sent non-JSON: %s", data)
				continue
			}
			f.mu.Lock()
			f.events = append(f.events, ev)
			f.mu.Unlock()
			f.handle(conn, ev)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLive) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeLive) send(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func (f *fakeLive) received(typ string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, ev := range f.events {
		if ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

// acceptStart is the minimal handler: answer session.start with session.started.
func acceptStart(f *fakeLive) func(conn *websocket.Conn, ev map[string]any) {
	return func(conn *websocket.Conn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(conn, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_test"}})
		case EventSessionClose:
			f.send(conn, map[string]any{"type": EventSessionClosed, "reason": "close_requested", "usage": map[string]any{"seconds": 3}})
		}
	}
}
