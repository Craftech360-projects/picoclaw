// pkg/realtimeconn/conn_test.go
package realtimeconn

import (
	"context"
	"errors"
	"net"
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

	// a secret straddling the 512-byte body limit must not leak a prefix either
	const long = "s3cretKEY-0123456789"
	for _, pad := range []int{500, 505, 510, 511} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(strings.Repeat("x", pad) + long + strings.Repeat("y", 600)))
		}))
		_, err := Dial(context.Background(), wsURL(srv), nil, long)
		srv.Close()
		if err == nil || strings.Contains(err.Error(), long[:2]) || strings.HasSuffix(err.Error(), long[:1]) {
			t.Fatalf("pad %d: err = %v, want no part of the secret", pad, err)
		}
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

// TestGoRejectsWorkOnceRunBeginsShuttingDownButEmitStillWorks pins the exact ordering Run's
// shutdown depends on: Go must stop accepting work (endingWork) strictly before Run calls
// work.Wait, while Emit/EmitAudio must keep working until the channels actually close (ended) —
// so already-running Go work and onEnd can still deliver their result. Driving endingWork
// directly (this file is package realtimeconn) makes the ordering deterministic instead of
// racing against Run's own goroutine scheduling.
func TestGoRejectsWorkOnceRunBeginsShuttingDownButEmitStillWorks(t *testing.T) {
	c := &Conn{
		audio:  make(chan []byte, 1),
		events: make(chan gptlive.Event, 1),
		done:   make(chan struct{}),
	}

	// Simulate Run having reached the point right before work.Wait(): no new Go work from here.
	c.workMu.Lock()
	c.endingWork = true
	c.workMu.Unlock()

	ran := make(chan struct{}, 1)
	c.Go(func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("Go ran fn after Run began shutting down (endingWork)")
	case <-time.After(100 * time.Millisecond):
	}

	// The session has not ended yet (ended is still false): Emit must still deliver, exactly as
	// it must for a Go-tracked goroutine still running, or for onEnd, at this same point in Run.
	c.Emit(gptlive.Closed{Reason: "still open"})
	select {
	case ev := <-c.events:
		if ev != (gptlive.Closed{Reason: "still open"}) {
			t.Fatalf("event = %#v", ev)
		}
	default:
		t.Fatal("Emit was rejected before ended was set")
	}
}

// TestSendTimesOutAndClosesTheSocketWhenTheVendorStopsReading pins the write deadline.
// The server upgrades and then never reads, so once the kernel buffers fill, a write has
// nowhere to go: without a deadline this blocks the caller (for Grok, the read goroutine)
// forever. Send must fail with a timeout instead, and must close the socket so Run's read
// loop ends rather than leaving a connection gorilla can no longer write a frame on.
func TestSendTimesOutAndClosesTheSocketWhenTheVendorStopsReading(t *testing.T) {
	defer func(d time.Duration) { writeTimeout = d }(writeTimeout)
	writeTimeout = 250 * time.Millisecond

	var up websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(5 * time.Second) // never reads: the client's writes back up
	}))
	defer srv.Close()

	ws, err := Dial(context.Background(), wsURL(srv), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(ws)
	ended := make(chan error, 1)
	go c.Run(func([]byte) {}, nil, func(err error) { ended <- err })

	payload := strings.Repeat("x", 256*1024)
	var sendErr error
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; sendErr == nil && time.Now().Before(deadline); i++ {
		sendErr = c.Send(map[string]any{"pad": payload})
	}
	if sendErr == nil {
		t.Fatal("Send never failed: no write deadline is in force")
	}
	var netErr net.Error
	if !errors.As(sendErr, &netErr) || !netErr.Timeout() {
		t.Fatalf("Send error = %v, want a write timeout", sendErr)
	}
	// The socket is closed, so the read loop ends on its own instead of holding a session
	// that can never speak again.
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end after the write deadline closed the socket")
	}
}
