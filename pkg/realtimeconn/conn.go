// Package realtimeconn is the websocket core the Grok Voice and Gemini Live clients share:
// serialized JSON writes, a read loop that never blocks on a slow consumer, tracked tool-call
// goroutines, and an optional redial that resumes a session on a new socket behind the same
// Audio and Events channels. Events use the gptlive vocabulary so gptLivePipeline handles
// every vendor the same way.
package realtimeconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const toolTimeout = 30 * time.Second // same bound as gptlive's delegated tool calls

// writeTimeout bounds one socket write. Without it a vendor that stops reading
// (TCP backpressure, a stalled proxy) blocks the writer forever behind writeMu —
// and with it every other writer, including a read loop that sends on the same
// socket. The session then goes mute with no audio, no barge-in, no Closed and
// no error, so nothing tears the room down and the child hears silence until the
// job ends. 10s is the upper end of what the vendors' own SDKs use and is orders
// of magnitude above a healthy write (the mic pushes a 100ms frame every 100ms).
// A var only so tests can shrink it.
var writeTimeout = 10 * time.Second

// SetWriteTimeoutForTest shrinks the per-write deadline and returns a function that
// restores it. Vendor-package tests that wedge a socket deliberately need the deadline
// inside their own budget, so that a wedged-write assertion can never be decided by the
// production 10s instead of by the behaviour under test. Nothing in production calls it;
// call it before the session is dialled and restore it after the session is closed, so
// no live writer reads the var while it changes.
func SetWriteTimeoutForTest(d time.Duration) func() {
	prev := writeTimeout
	writeTimeout = d
	return func() { writeTimeout = prev }
}

// Dial opens a websocket. A refused upgrade is reported with the vendor's error body
// (for example xAI's "used all available credits"), and secret is scrubbed from every
// error because Gemini's key rides in the URL.
func Dial(ctx context.Context, url string, header http.Header, secret string) (*websocket.Conn, error) {
	ws, resp, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err == nil {
		return ws, nil
	}
	scrub := func(s string) string {
		if secret == "" {
			return s
		}
		return strings.ReplaceAll(s, secret, "***")
	}
	if resp != nil {
		const maxBody = 512
		// scrub before cutting: read enough that a secret straddling the cut is still whole
		body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBody+len(secret))))
		msg := scrub(string(body))
		if len(msg) > maxBody {
			msg = msg[:maxBody]
		}
		// a secret cut off by the read limit or the cut is left as a trailing prefix: mask it
		for i := len(secret) - 1; i > 0; i-- {
			if strings.HasSuffix(msg, secret[:i]) {
				msg = msg[:len(msg)-i] + "***"
				break
			}
		}
		return nil, fmt.Errorf("websocket handshake refused: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(msg))
	}
	return nil, errors.New(scrub(err.Error()))
}

// Conn outlives any single websocket: Run can swap the socket and keep feeding the same channels.
type Conn struct {
	mu      sync.Mutex // guards ws, sock, secret and closed
	ws      *websocket.Conn
	sock    *socketStats // diagnostics for ws; swapped with it
	secret  string       // scrubbed from every logged error (SetSecret)
	closed  bool
	writeMu sync.Mutex
	audio   chan []byte
	events  chan gptlive.Event
	work    sync.WaitGroup

	workMu     sync.Mutex // guards endingWork; makes Go's check-and-Add atomic with Run's shutdown
	endingWork bool       // set before Run calls work.Wait(): no new Go work is accepted from here on

	endMu sync.RWMutex // guards ended; Run holds the write lock while flipping it and closing the channels
	ended bool
	done  chan struct{}
}

// socketStats is per-socket diagnostics for the "vendor socket ended" log line.
type socketStats struct {
	seq       int
	opened    time.Time
	frames    int64     // Run's goroutine only
	lastFrame time.Time // Run's goroutine only
	sendFails atomic.Int64
}

func New(ws *websocket.Conn) *Conn {
	return &Conn{
		ws:     ws,
		sock:   &socketStats{seq: 1, opened: time.Now()},
		audio:  make(chan []byte, 1024),
		events: make(chan gptlive.Event, 1024),
		done:   make(chan struct{}),
	}
}

// SetSecret names the API key to scrub (raw and URL-escaped) from the errors and close
// reasons Conn logs. Call it right after New.
func (c *Conn) SetSecret(secret string) {
	c.mu.Lock()
	c.secret = secret
	c.mu.Unlock()
}

// Scrub masks the SetSecret key (raw and URL-escaped) in s, for text a session puts into an
// emitted error.
func (c *Conn) Scrub(s string) string { return c.scrub(s) }

func (c *Conn) scrub(s string) string {
	c.mu.Lock()
	secret := c.secret
	c.mu.Unlock()
	return scrubSecret(s, secret)
}

func (c *Conn) Audio() <-chan []byte         { return c.audio }
func (c *Conn) Events() <-chan gptlive.Event { return c.events }
func (c *Conn) Done() <-chan struct{}        { return c.done }

// WriteJSON writes one JSON message on a socket a Conn does not own yet, under the
// same writeTimeout Send applies. The setup frame and the resume-time toolResponse
// replay are both written inside geminilive's connect, on the Run goroutine, while
// the Conn still holds the socket being replaced — so they cannot go through Send,
// and without a deadline they were the last writes that could wedge a session with
// no reads. The deadline is cleared afterwards so the socket handed back carries
// none of it.
//
// A timed-out write closes the socket, the same contract Send keeps and for the same
// reason: gorilla cannot resynchronise after a write deadline fires, so the socket
// reads fine and can never write again. Handing one of those back to Run would be
// worse than in Send's case — the read loop would keep the call looking alive while
// every mic write failed silently, with nothing to tear the room down.
func WriteJSON(ws *websocket.Conn, v any) error {
	_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := ws.WriteJSON(v)
	_ = ws.SetWriteDeadline(time.Time{})
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		_ = ws.Close()
	}
	return err
}

// Send writes one JSON message on the current socket, under writeTimeout: a write
// that cannot complete in that time fails instead of blocking its caller (and every
// other writer) forever.
//
// A timed-out write also closes the socket. gorilla leaves a connection unusable
// after a write deadline fires — it cannot resynchronise a half-written frame — so
// leaving it open would only trade a blocked goroutine for a socket that reads fine
// and can never speak again, which is the same permanent silence one step removed.
// Closing it ends Run's read loop, which redials (Gemini) or emits the
// non-recoverable error that tears the room session down (Grok).
func (c *Conn) Send(v any) error {
	c.mu.Lock()
	ws, sock := c.ws, c.sock
	c.mu.Unlock()
	c.writeMu.Lock()
	_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := ws.WriteJSON(v)
	c.writeMu.Unlock()
	if err == nil {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		_ = ws.Close()
	}
	if sock.sendFails.Add(1) == 1 {
		// once per socket: the mic pushes every 100ms, and the socket-end line carries the count
		logger.WarnCF("realtime", "vendor socket send failed; later failures on this socket are only counted", map[string]any{
			"socket_seq": sock.seq, "error": truncate(c.scrub(err.Error())),
		})
	}
	return err
}

// EmitAudio hands model PCM to the consumer without ever blocking the read loop. A no-op once
// Run has ended the session (channels closed) — a straggler goroutine (a timer, keepalive, or
// pipeline-side helper not started via Go) must not panic on a closed channel.
func (c *Conn) EmitAudio(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	c.endMu.RLock()
	defer c.endMu.RUnlock()
	if c.ended {
		return
	}
	select {
	case c.audio <- pcm:
	default: // ponytail: ~20s of backlog; dropping beats stalling the socket read
		logger.WarnCF("realtime", "model audio backlog full; dropping audio", map[string]any{"bytes": len(pcm)})
	}
}

// Emit hands an event to the consumer without ever blocking the read loop. A no-op once Run has
// ended the session (channels closed); see EmitAudio.
func (c *Conn) Emit(ev gptlive.Event) {
	c.endMu.RLock()
	defer c.endMu.RUnlock()
	if c.ended {
		return
	}
	select {
	case c.events <- ev:
	default:
		logger.WarnCF("realtime", "event backlog full; dropping event", map[string]any{"type": fmt.Sprintf("%T", ev)})
	}
}

// Go runs fn (a tool call) on its own goroutine; Run waits for it (work.Wait) before it stops
// accepting new work and, later, closes the channels. Once Run has begun shutting down — before
// it calls work.Wait, so already-running Go work can still Emit — Go neither runs fn nor touches
// the WaitGroup. The check and the Add share workMu, so there is no Add-after-Wait race no matter
// when a caller not tracked by Run (a timer, keepalive, or pipeline-side helper) calls Go.
func (c *Conn) Go(fn func()) {
	c.workMu.Lock()
	if c.endingWork {
		c.workMu.Unlock()
		return
	}
	c.work.Add(1)
	c.workMu.Unlock()
	go func() {
		defer c.work.Done()
		fn()
	}()
}

// Closing reports whether Close was called, i.e. whether a socket end is expected.
func (c *Conn) Closing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Conn) Close() error {
	c.mu.Lock()
	c.closed = true
	ws := c.ws
	c.mu.Unlock()
	return ws.Close()
}

// Run reads frames into handle until the socket ends. If redial is non-nil and Close was not
// called, it swaps in redial's socket and keeps reading — unless the socket being replaced
// delivered no frames at all, which ends the session instead of redialing in a tight loop (a
// vendor that accepts and instantly closes). The replaced/ended socket is always closed. At the
// end it stops accepting new Go work, waits for Go work already running (which may still Emit),
// calls onEnd (which may still Emit) with the last read error, then closes Audio, Events and Done.
func (c *Conn) Run(handle func([]byte), redial func() (*websocket.Conn, error), onEnd func(err error)) {
	var last error
	for {
		c.mu.Lock()
		ws, sock := c.ws, c.sock
		c.mu.Unlock()
		gotFrame := false
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				last = err
				break
			}
			gotFrame = true
			sock.frames++
			sock.lastFrame = time.Now()
			handle(data)
		}
		_ = ws.Close() // release the replaced/ended socket; a redial uses a new one
		closing := c.Closing()
		c.logSocketEnd(sock, last, closing, !closing && redial != nil && gotFrame)
		if closing || redial == nil {
			break
		}
		if !gotFrame {
			logger.WarnCF("realtime", "vendor socket closed without delivering any frames; not redialing", nil)
			break
		}
		dialStart := time.Now()
		next, err := redial()
		if err != nil {
			logger.WarnCF("realtime", "vendor socket redial failed", map[string]any{
				"socket_seq": sock.seq + 1, "redial_ms": msSince(dialStart), "error": truncate(c.scrub(err.Error())),
			})
			last = err
			break
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			_ = next.Close()
			break
		}
		c.ws = next
		c.sock = &socketStats{seq: sock.seq + 1, opened: time.Now()}
		c.mu.Unlock()
		logger.InfoCF("realtime", "vendor socket resumed on a new connection", map[string]any{
			"socket_seq": sock.seq + 1, "redial_ms": msSince(dialStart),
		})
	}
	c.workMu.Lock()
	c.endingWork = true
	c.workMu.Unlock()
	c.work.Wait()
	if onEnd != nil {
		onEnd(last)
	}
	c.endMu.Lock()
	c.ended = true
	close(c.audio)
	close(c.events)
	c.endMu.Unlock()
	close(c.done)
}

// logSocketEnd is the one line per socket end: why it ended (close code and reason when the
// vendor sent a close frame) and what the socket did before, so a live run shows whether the
// vendor closed on a deadline, a protocol error or a network reset.
func (c *Conn) logSocketEnd(sock *socketStats, err error, closing, willRedial bool) {
	c.mu.Lock()
	secret := c.secret
	c.mu.Unlock()
	code, text := closeDetails(secret, err)
	fields := map[string]any{
		"socket_seq":    sock.seq,
		"close_code":    code,
		"close_text":    text,
		"socket_age_ms": msSince(sock.opened),
		"frames":        sock.frames,
		"idle_ms":       msSince(sock.lastFrame),
		"send_failures": sock.sendFails.Load(),
		"closing":       closing,
		"will_redial":   willRedial,
	}
	if closing {
		logger.InfoCF("realtime", "vendor socket ended", fields)
		return
	}
	logger.WarnCF("realtime", "vendor socket ended", fields)
}

// RunTool executes one model tool call against the session's registry executor.
func RunTool(exec gptlive.ToolExecutor, name string, args map[string]any) (string, bool) {
	if exec == nil {
		return "tools are not available in this session", true
	}
	if args == nil {
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	return exec.Execute(ctx, name, args)
}
