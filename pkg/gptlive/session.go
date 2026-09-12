package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// ToolExecutor runs a function call the backend model asked for.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (output string, isError bool)
}

type Config struct {
	APIKey       string
	BaseURL      string // default DefaultBaseURL
	Model        string // default DefaultModel
	Voice        any    // name or map{"id": ...}; default DefaultVoice
	Instructions string // the voice persona; immutable after start
	History      []InputItem
	SampleRate   int // 16000 or 24000
	Backend      ResponsesConfig
	Tools        ToolExecutor // nil: function calls are answered with an error output

	MaxSessionDuration time.Duration // 0: never reconnect on a timer
	MaxRetries         int           // default 3
	RetryInterval      time.Duration // default 2s
	DialTimeout        time.Duration // default 10s
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Voice == nil || c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.SampleRate == 0 {
		c.SampleRate = 24000
	}
	if c.Backend.Model == "" {
		c.Backend.Model = DefaultBackendModel
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = 2 * time.Second
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
	return c
}

// Event is one of the concrete event types below.
type Event interface{ isEvent() }

type SessionStarted struct{ ID string }
type Closed struct {
	Reason       string
	VoiceSeconds float64
}
type Error struct {
	Err         error
	Recoverable bool
}
type VoiceUsage struct{ Seconds float64 }

func (SessionStarted) isEvent() {}
func (Closed) isEvent()         {}
func (Error) isEvent()          {}
func (VoiceUsage) isEvent()     {}

const sessionCloseTimeout = 5 * time.Second

type Session struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	out    chan []byte // marshalled client events, written after session.started
	events chan Event
	audio  chan []byte // PCM16 output frames

	mu        sync.Mutex
	sessionID string
	startSent bool
	closing   bool
	started   chan struct{} // closed when session.started arrives
	closedEv  chan struct{} // closed when session.closed arrives

	historyMu sync.Mutex
	history   []InputItem
	speech    map[string]*speech

	delegations      map[string]*delegatedResponse
	callToDelegation map[string]string

	done chan struct{} // closed when the run loop exits
}

// Dial connects, sends session.start and returns once the service answers with
// session.started. The returned session reconnects on its own until Close.
func Dial(ctx context.Context, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		cfg: cfg, ctx: sctx, cancel: cancel,
		out: make(chan []byte, 512), events: make(chan Event, 256), audio: make(chan []byte, 1024),
		started: make(chan struct{}), closedEv: make(chan struct{}),
		history: append([]InputItem(nil), cfg.History...), speech: map[string]*speech{},
		delegations: map[string]*delegatedResponse{}, callToDelegation: map[string]string{},
		done: make(chan struct{}),
	}
	go s.run()
	select {
	case <-s.started:
		return s, nil
	case <-s.done:
		cancel()
		return nil, errors.New("gptlive: session ended before it started")
	case <-ctx.Done():
		s.cancel()
		return nil, ctx.Err()
	}
}

func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *Session) Events() <-chan Event { return s.events }
func (s *Session) Audio() <-chan []byte { return s.audio }

// PushAudio queues PCM16 mono at Config.SampleRate. Frames are dropped, with a log
// line, only when the outgoing queue is full for over a second.
func (s *Session) PushAudio(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	s.send(inputAudioAppendEvent{Type: EventInputAudioAppend, Audio: base64.StdEncoding.EncodeToString(pcm)})
}

func (s *Session) AppendInstructions(text string) {
	s.send(encodeAppend(EventInstructionsAppend, text, nil))
}
func (s *Session) AppendThinking(text string) { s.send(encodeAppend(EventThinkingAppend, text, nil)) }
func (s *Session) AppendCommentary(text string) {
	s.send(encodeAppend(EventCommentaryAppend, text, nil))
}
func (s *Session) MuteInput() {
	s.send(simpleEvent{Type: EventInputAudioMute, EventID: newEventID("mute_")})
}
func (s *Session) UnmuteInput() {
	s.send(simpleEvent{Type: EventInputAudioUnmute, EventID: newEventID("unmute_")})
}

func (s *Session) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		logger.WarnCF("gptlive", "marshal client event", map[string]any{"error": err.Error()})
		return
	}
	select {
	case s.out <- data:
	case <-s.ctx.Done():
	case <-time.After(time.Second):
		logger.WarnCF("gptlive", "dropping client event: send queue full", nil)
	}
}

func (s *Session) emit(ev Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *Session) startConfig() SessionConfig {
	s.historyMu.Lock()
	hist := append([]InputItem(nil), s.history...)
	s.historyMu.Unlock()
	if len(hist) > maxInputItems {
		hist = hist[len(hist)-maxInputItems:]
	}
	backend := s.cfg.Backend
	return SessionConfig{
		Model:        s.cfg.Model,
		Instructions: s.cfg.Instructions,
		Input:        hist,
		Audio:        &AudioConfig{Format: &AudioFormat{Type: "audio/pcm", Rate: s.cfg.SampleRate}, Output: &AudioOutput{Voice: s.cfg.Voice}},
		Delegation:   &Delegation{Type: "responses", Responses: &backend},
	}
}

// Close asks the service to end the session, waits for its final usage, and stops.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		<-s.done
		return nil
	}
	s.closing = true
	startSent := s.startSent
	s.mu.Unlock()
	if startSent {
		s.send(simpleEvent{Type: EventSessionClose, EventID: newEventID("close_")})
		select {
		case <-s.closedEv:
		case <-time.After(sessionCloseTimeout):
		case <-ctx.Done():
		}
	}
	s.cancel()
	<-s.done
	return nil
}

func dialWebsocket(ctx context.Context, cfg Config) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer " + cfg.APIKey}, "User-Agent": {"picoclaw-gptlive"}}
	conn, _, err := websocket.DefaultDialer.DialContext(dctx, cfg.BaseURL, headers)
	if err != nil {
		return nil, fmt.Errorf("gptlive: dial: %w", err)
	}
	return conn, nil
}

// runOnce drives one websocket connection until it fails or the session closes.
// It returns nil when the close was ours, an error otherwise.
func (s *Session) runOnce(conn *websocket.Conn) error {
	defer conn.Close()
	start := sessionStartEvent{Type: EventSessionStart, EventID: newEventID("session_start_"), Session: s.startConfig()}
	data, _ := json.Marshal(start)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("gptlive: send session.start: %w", err)
	}
	s.mu.Lock()
	s.startSent = true
	started := s.started
	s.mu.Unlock()

	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			var ev ServerEvent
			if err := json.Unmarshal(msg, &ev); err != nil {
				logger.WarnCF("gptlive", "bad server event", map[string]any{"error": err.Error()})
				continue
			}
			if fatal := s.handleEvent(ev); fatal != nil {
				readErr <- fatal
				return
			}
		}
	}()

	var writeMu sync.Mutex
	write := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, b)
	}
	// stopReader closes the connection to unblock a pending ReadMessage and waits for
	// the read goroutine to exit. Every return path that has not already consumed
	// readErr must call this before returning, so the caller (run) never closes
	// s.events/s.audio while the read goroutine could still be inside handleEvent
	// sending on them — a send on an already-closed channel always panics.
	stopReader := func() {
		conn.Close()
		<-readErr
	}
	// hold every client event until session.started, as the protocol requires
	select {
	case <-started:
	case err := <-readErr:
		return s.classify(err)
	case <-s.ctx.Done():
		stopReader()
		return nil
	}
	var timer <-chan time.Time
	if s.cfg.MaxSessionDuration > 0 {
		timer = time.After(s.cfg.MaxSessionDuration)
	}
	for {
		select {
		case b := <-s.out:
			if err := write(b); err != nil {
				stopReader()
				return fmt.Errorf("gptlive: write: %w", err)
			}
		case err := <-readErr:
			return s.classify(err)
		case <-timer:
			stopReader()
			return errReconnectTimer
		case <-s.ctx.Done():
			stopReader()
			return nil
		}
	}
}

var errReconnectTimer = errors.New("gptlive: max session duration reached")

// FatalProtocolError is returned from runOnce when the service reports an
// unrecoverable protocol error (see ErrorBody.Fatal). Task 6's retry loop inspects it
// via errors.As to decide not to reconnect.
type FatalProtocolError struct {
	Body *ErrorBody
}

func (e *FatalProtocolError) Error() string {
	return fmt.Sprintf("gptlive: %s (%s): %s", e.Body.Type, e.Body.Code, e.Body.Message)
}

// classify turns a read failure into nil when the close was ours, and passes a fatal
// protocol error through unchanged so run() can emit it as the one Error for this
// failure and Task 6 can recognize it.
func (s *Session) classify(err error) error {
	var fatal *FatalProtocolError
	if errors.As(err, &fatal) {
		return fatal
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	select {
	case <-s.closedEv:
		return nil
	default:
	}
	if closing {
		return nil
	}
	return fmt.Errorf("gptlive: connection closed unexpectedly: %w", err)
}

func (s *Session) run() {
	defer close(s.done)
	defer close(s.events)
	defer close(s.audio)
	conn, err := dialWebsocket(s.ctx, s.cfg)
	if err != nil {
		s.emit(Error{Err: err, Recoverable: false})
		return
	}
	if err := s.runOnce(conn); err != nil {
		s.emit(Error{Err: err, Recoverable: false})
	}
}

// handleEvent runs on the read goroutine. It returns a non-nil error only for
// conditions that must end this connection.
func (s *Session) handleEvent(ev ServerEvent) error {
	switch ev.Type {
	case EventSessionStarted:
		s.mu.Lock()
		if ev.Session != nil {
			s.sessionID = ev.Session.ID
		}
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		s.mu.Unlock()
		s.emit(SessionStarted{ID: s.ID()})
	case EventOutputAudioDelta:
		if ev.Delta == "" {
			return nil
		}
		pcm, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil || len(pcm) == 0 {
			return nil
		}
		select {
		case s.audio <- pcm:
		case <-s.ctx.Done():
		}
	case EventInputTranscriptDelta:
		s.onTranscriptDelta("user", ev)
	case EventOutputTranscriptDelta:
		s.onTranscriptDelta("assistant", ev)
	case EventDelegationCreated:
		s.onDelegationCreated(ev)
	case EventResponseEvent:
		s.onResponseEvent(ev)
	case EventSessionUsageUpdated:
		if ev.Usage != nil {
			s.emit(VoiceUsage{Seconds: ev.Usage.Seconds})
		}
	case EventSessionClosed:
		var secs float64
		if ev.Usage != nil {
			secs = ev.Usage.Seconds
		}
		s.emit(Closed{Reason: ev.Reason, VoiceSeconds: secs})
		s.mu.Lock()
		select {
		case <-s.closedEv:
		default:
			close(s.closedEv)
		}
		s.mu.Unlock()
	case EventError:
		if ev.Error == nil {
			return nil
		}
		if ev.Error.Fatal() {
			// Returned, not emitted: the read loop hands this to classify, and run()
			// emits it exactly once. Emitting here too would double-report one failure.
			return &FatalProtocolError{Body: ev.Error}
		}
		err := fmt.Errorf("gptlive: %s (%s): %s", ev.Error.Type, ev.Error.Code, ev.Error.Message)
		s.emit(Error{Err: err, Recoverable: true})
	}
	return nil
}
