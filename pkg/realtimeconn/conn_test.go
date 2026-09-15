// pkg/realtimeconn/conn_test.go
package realtimeconn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
)

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func TestRunRedialsThroughTheSameChannelsAndClosesThemOnce(t *testing.T) {
	var conns atomic.Int32
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		n := conns.Add(1)
		_ = c.WriteMessage(websocket.TextMessage, []byte{byte('0' + n)})
		if n == 2 {
			time.Sleep(time.Second) // stay open until the client closes
		}
	}))
	defer srv.Close()

	ws, err := Dial(context.Background(), wsURL(srv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(ws)
	got := make(chan string, 4)
	ended := make(chan error, 1)
	go c.Run(func(b []byte) {
		got <- string(b)
		c.EmitAudio(b)
	}, func() (*websocket.Conn, error) {
		return Dial(context.Background(), wsURL(srv), nil, "")
	}, func(err error) {
		c.Emit(gptlive.Closed{Reason: "test"})
		ended <- err
	})

	for _, want := range []string{"1", "2"} {
		select {
		case b := <-got:
			if b != want {
				t.Fatalf("message = %q, want %q", b, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no message %q: redial did not happen", want)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	<-ended
	if ev := <-c.Events(); ev != (gptlive.Closed{Reason: "test"}) {
		t.Fatalf("onEnd event = %#v", ev)
	}
	if _, ok := <-c.Events(); ok {
		t.Fatal("Events still open after Run returned")
	}
	for range c.Audio() { // drains the two audio frames, then must be closed
	}
}

func TestDialErrorNeverEchoesTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key s3cret"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), wsURL(srv)+"?key=s3cret", nil, "s3cret")
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err = %v, want an HTTP 403 error with the secret scrubbed", err)
	}
}

type echoExec struct{}

func (echoExec) Execute(_ context.Context, name string, args map[string]any) (string, bool) {
	return name + ":" + args["x"].(string), false
}

func TestRunTool(t *testing.T) {
	if out, isErr := RunTool(echoExec{}, "t", map[string]any{"x": "1"}); out != "t:1" || isErr {
		t.Fatalf("RunTool = %q, %v", out, isErr)
	}
	if _, isErr := RunTool(nil, "t", nil); !isErr {
		t.Fatal("a nil executor must report an error output")
	}
}

func TestEmitAudioEmitAndGoAfterRunEndedDoNotPanic(t *testing.T) {
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.Close() // accept then instantly close: Run ends with no redial configured
	}))
	defer srv.Close()

	ws, err := Dial(context.Background(), wsURL(srv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(ws)
	runDone := make(chan struct{})
	go func() {
		c.Run(func([]byte) {}, nil, nil)
		close(runDone)
	}()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not end")
	}
	<-c.Done()

	// None of these may panic (send on a closed channel) now that the session has ended.
	c.EmitAudio([]byte("late"))
	c.Emit(gptlive.Closed{Reason: "late"})

	ran := make(chan struct{}, 1)
	c.Go(func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("Go ran fn after Run had already ended the session")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunStopsRedialingASocketThatDeliveredNoFrames(t *testing.T) {
	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c.Close() // accepts, but never sends a frame before closing
	}))
	defer srv.Close()

	ws, err := Dial(context.Background(), wsURL(srv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(ws)
	var redials atomic.Int32
	runDone := make(chan struct{})
	go func() {
		c.Run(func([]byte) {}, func() (*websocket.Conn, error) {
			redials.Add(1)
			return Dial(context.Background(), wsURL(srv), nil, "")
		}, nil)
		close(runDone)
	}()

	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run kept redialing an empty socket instead of ending the session")
	}
	if n := redials.Load(); n != 0 {
		t.Fatalf("redial called %d times, want 0: a socket with no frames must not be redialed", n)
	}
}
