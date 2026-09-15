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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const toolTimeout = 30 * time.Second // same bound as gptlive's delegated tool calls

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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("websocket handshake refused: HTTP %d: %s", resp.StatusCode, scrub(strings.TrimSpace(string(body))))
	}
	return nil, errors.New(scrub(err.Error()))
}

// Conn outlives any single websocket: Run can swap the socket and keep feeding the same channels.
type Conn struct {
	mu      sync.Mutex // guards ws and closed
	ws      *websocket.Conn
	closed  bool
	writeMu sync.Mutex
	audio   chan []byte
	events  chan gptlive.Event
	work    sync.WaitGroup
	done    chan struct{}
}

func New(ws *websocket.Conn) *Conn {
	return &Conn{
		ws:     ws,
		audio:  make(chan []byte, 1024),
		events: make(chan gptlive.Event, 1024),
		done:   make(chan struct{}),
	}
}

func (c *Conn) Audio() <-chan []byte         { return c.audio }
func (c *Conn) Events() <-chan gptlive.Event { return c.events }
func (c *Conn) Done() <-chan struct{}        { return c.done }

// Send writes one JSON message on the current socket.
func (c *Conn) Send(v any) error {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return ws.WriteJSON(v)
}

// EmitAudio hands model PCM to the consumer without ever blocking the read loop.
func (c *Conn) EmitAudio(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	select {
	case c.audio <- pcm:
	default: // ponytail: ~20s of backlog; dropping beats stalling the socket read
		logger.WarnCF("realtime", "model audio backlog full; dropping audio", map[string]any{"bytes": len(pcm)})
	}
}

// Emit hands an event to the consumer without ever blocking the read loop.
func (c *Conn) Emit(ev gptlive.Event) {
	select {
	case c.events <- ev:
	default:
		logger.WarnCF("realtime", "event backlog full; dropping event", map[string]any{"type": fmt.Sprintf("%T", ev)})
	}
}

// Go runs fn (a tool call) on its own goroutine; Run closes the channels only after it returns.
func (c *Conn) Go(fn func()) {
	c.work.Add(1)
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
// called, it swaps in redial's socket and keeps reading. At the end it waits for Go work,
// calls onEnd (which may still Emit) with the last read error, then closes Audio, Events and Done.
func (c *Conn) Run(handle func([]byte), redial func() (*websocket.Conn, error), onEnd func(err error)) {
	var last error
	for {
		c.mu.Lock()
		ws := c.ws
		c.mu.Unlock()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				last = err
				break
			}
			handle(data)
		}
		if c.Closing() || redial == nil {
			break
		}
		next, err := redial()
		if err != nil {
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
		c.mu.Unlock()
		logger.InfoCF("realtime", "vendor socket resumed on a new connection", nil)
	}
	c.work.Wait()
	if onEnd != nil {
		onEnd(last)
	}
	close(c.audio)
	close(c.events)
	close(c.done)
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
