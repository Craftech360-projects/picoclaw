package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

	speechMu sync.Mutex // guards speech across the read goroutine and idle timers
	speech   map[string]*speech

	delegationsMu    sync.Mutex // guards delegations and callToDelegation across the read goroutine and tool goroutines
	delegations      map[string]*delegatedResponse
	callToDelegation map[string]string
	toolWG           sync.WaitGroup // one per in-flight executeCall goroutine; see closeChannelsWhenIdle

	// audioQueue/eventQueue decouple the read goroutine from s.audio/s.events:
	// handleEvent pushes into these (which never blocks — see handoff.go) and
	// a dedicated feeder goroutine each (started in Dial, stopped in
	// closeChannelsWhenIdle) drains them into the real channels, which IS
	// allowed to block on a slow consumer. This is the fix for the root cause
	// documented on handleEvent's EventOutputAudioDelta case: before this, a
	// blocking send straight into s.audio from the read goroutine let a slow
	// downstream consumer stall the socket read itself, which OpenAI's own
	// TCP backpressure then throttled — measured on the dev box as our
	// pipeline's consumption rate, misread as OpenAI's delivery rate.
	//
	// Both are nil on a bare &Session{} built directly by a test (see
	// transcript_test.go/delegation_test.go's newTestSession) rather than
	// through Dial; emit and handleEvent fall back to the old direct-select
	// behaviour in that case, which is what those tests were already written
	// against and still exercises the exact same event-building logic.
	audioQueue      *handoffQueue[[]byte]
	audioFeederStop chan struct{}
	audioFeederDone chan struct{}
	audioDropLog    dropRateLimiter

	eventQueue      *handoffQueue[Event]
	eventFeederStop chan struct{}
	eventFeederDone chan struct{}
	eventDropLog    dropRateLimiter

	done chan struct{} // closed when the run loop exits
}

// audioHandoffCapSeconds bounds how much undelivered model audio audioQueue
// will hold when Audio()'s consumer falls behind, so a stalled consumer leaks
// a few seconds of PCM, never an unbounded amount. A few seconds is already
// generous slack — driveSegmenter's elastic playout (pkg/livekit) holds at most
// a few hundred milliseconds — and is chosen in SECONDS, not
// bytes or chunk count, so the actual memory bound (computed in Dial, once
// the session's sample rate is known) means the same thing at 16kHz and
// 24kHz.
const audioHandoffCapSeconds = 3 * time.Second

// eventHandoffCap bounds how many undelivered Events eventQueue will hold
// when Events()'s consumer falls behind. Events arrive far less densely than
// audio (at most one per transcript delta, tool call, or usage update, versus
// one audio delta roughly every few tens of milliseconds), so a count-based
// cap this generous is only ever reached by a consumer that has stopped
// reading entirely for a long time — at which point dropping the oldest
// backlog first is the same trade audioQueue makes: current information over
// stale.
const eventHandoffCap = 4096

// dropLogInterval is how often a sustained overflow (audio or events) is
// allowed to log at all; see dropRateLimiter.
const dropLogInterval = 5 * time.Second

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
	audioCapBytes := int64(audioHandoffCapSeconds.Seconds() * float64(cfg.SampleRate) * 2) // PCM16 mono: 2 bytes/sample
	s.audioDropLog = dropRateLimiter{interval: dropLogInterval}
	s.audioQueue = newHandoffQueue[[]byte](audioCapBytes, func(pcm []byte) int64 { return int64(len(pcm)) }, func(items int, bytes int64) {
		if ok, totalItems, totalBytes := s.audioDropLog.add(items, bytes); ok {
			logger.WarnCF("gptlive", "dropping oldest buffered audio: downstream audio consumer is stalled", map[string]any{
				"dropped_chunks": totalItems,
				"dropped_bytes":  totalBytes,
				"dropped_ms":     float64(totalBytes) / float64(cfg.SampleRate*2) * 1000,
				"sample_rate":    cfg.SampleRate,
				"cap_seconds":    audioHandoffCapSeconds.Seconds(),
			})
		}
	})
	s.audioFeederStop = make(chan struct{})
	s.audioFeederDone = make(chan struct{})

	s.eventDropLog = dropRateLimiter{interval: dropLogInterval}
	s.eventQueue = newHandoffQueue[Event](eventHandoffCap, func(Event) int64 { return 1 }, func(items int, _ int64) {
		if ok, totalItems, _ := s.eventDropLog.add(items, 0); ok {
			logger.WarnCF("gptlive", "dropping oldest buffered event: downstream event consumer is stalled", map[string]any{
				"dropped_events": totalItems,
				"cap_events":     eventHandoffCap,
			})
		}
	})
	s.eventFeederStop = make(chan struct{})
	s.eventFeederDone = make(chan struct{})

	// flushOnStop=false for audio (drop whatever is still queued at teardown —
	// see feedHandoff's own doc comment), true for events (a final Error/
	// Closed/FunctionResult must still be delivered; see the same comment).
	go feedHandoff(s.audioQueue, s.audio, s.audioFeederStop, s.audioFeederDone, false)
	go feedHandoff(s.eventQueue, s.events, s.eventFeederStop, s.eventFeederDone, true)
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
	s.send(encodeAppend(EventInstructionsAppend, boundAppendContent("instructions", text), nil))
}
func (s *Session) AppendThinking(text string) {
	s.send(encodeAppend(EventThinkingAppend, boundAppendContent("thinking", text), nil))
}
func (s *Session) AppendCommentary(text string) {
	s.send(encodeAppend(EventCommentaryAppend, boundAppendContent("commentary", text), nil))
}

// maxAppendChars is a defensive cap on every context append this session sends
// (session.instructions.append, session.thinking.append, session.commentary.append),
// enforced here because the 500-token-per-append limit is a fact of the GPT-Live
// protocol itself, not something every caller can be trusted to respect. A real
// session hit it in production: "gptlive: invalid_request_error (invalid_value):
// Context append text must not exceed 500 tokens", with the append rejected
// outright and the child never greeted. The same path also carries the quiz
// Door directives pushed through AppendInstructions, which accumulate in size
// across a session as each directive declares itself superseding the last.
//
// Tokenizers vary by content and language, so this estimates a generous ~4
// characters per token and keeps only 80% of that budget as headroom against
// the estimate being wrong in either direction: 500 tokens * 4 chars/token *
// 0.8 = 1600 characters. A truncated append that still reaches the model is
// strictly better than a rejected one that leaves a child in silence.
const maxAppendChars = 1600

// boundAppendContent truncates content that would risk exceeding the service's
// per-append token cap, logging which channel was truncated and by how much —
// never the content itself, which may carry a child's own words or an API key
// fragment injected upstream. Content within budget passes through unchanged.
func boundAppendContent(channel, content string) string {
	if utf8.RuneCountInString(content) <= maxAppendChars {
		return content
	}
	kept := truncateAppendContent(content, maxAppendChars)
	logger.WarnCF("gptlive", "truncating oversized context append", map[string]any{
		"channel":         channel,
		"original_chars":  utf8.RuneCountInString(content),
		"kept_chars":      utf8.RuneCountInString(kept),
		"truncated_chars": utf8.RuneCountInString(content) - utf8.RuneCountInString(kept),
	})
	return kept
}

// truncateAppendContent cuts content to at most n runes, preferring to end at a
// sentence boundary (. ! ?) and falling back to a word boundary (whitespace) so
// the result reads as a complete-ish thought rather than stopping mid-word.
// Slicing on runes (not bytes) keeps a multi-byte character — Hindi text is a
// realistic case here, given the language lock GPT-Live personas can carry —
// from being split in the middle.
func truncateAppendContent(content string, n int) string {
	runes := []rune(content)
	if n <= 0 || len(runes) <= n {
		return content
	}
	cut := string(runes[:n])
	if idx := strings.LastIndexAny(cut, ".!?"); idx > 0 {
		return strings.TrimSpace(cut[:idx+1])
	}
	if idx := strings.LastIndexAny(cut, " \n\t"); idx > 0 {
		return strings.TrimSpace(cut[:idx])
	}
	return strings.TrimSpace(cut)
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

// emit hands ev to whatever is reading Events(). When s.eventQueue exists
// (every Session built by Dial), this never blocks — see eventQueue's own doc
// comment on the Session struct for why that matters when emit is called from
// the read goroutine. Falls back to the old direct-select behaviour when
// eventQueue is nil, which is only the case for a bare &Session{} a test
// builds without going through Dial (see the struct field's comment).
func (s *Session) emit(ev Event) {
	if s.eventQueue != nil {
		s.eventQueue.push(ev)
		return
	}
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
	// Only ask the service to end the session while the run loop is still
	// there to write it. On the unrecoverable-error path (MaxRetries exhausted,
	// or a fatal protocol error) run has already returned and closed s.done, so
	// nothing is left reading s.out: session.close would go into a queue nobody
	// drains, and the wait below would then burn the full sessionCloseTimeout
	// waiting for a session.closed that can never arrive. That is a fixed 5s of
	// teardown on every failed session. s.done is also one of the wait cases,
	// for the run loop that exits between this check and the wait.
	select {
	case <-s.done:
		startSent = false
	default:
	}
	if startSent {
		s.send(simpleEvent{Type: EventSessionClose, EventID: newEventID("close_")})
		select {
		case <-s.closedEv:
		case <-s.done:
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

// run dials, drives one connection via runOnce until it fails, and either reconnects
// (retrying with backoff, or immediately after a MaxSessionDuration timer) or gives up.
// It never retries a fatal protocol error, and it never retries past cfg.MaxRetries.
func (s *Session) run() {
	defer close(s.done)
	defer s.closeChannelsWhenIdle()
	retries := 0
	reconnecting := false
	for {
		if s.ctx.Err() != nil {
			return
		}
		conn, err := dialWebsocket(s.ctx, s.cfg)
		if err == nil {
			if reconnecting {
				s.resetForReconnect()
				s.emit(Reconnected{})
			}
			err = s.runOnce(conn)
			if err == nil {
				return // our own close, or ctx cancelled
			}
			if errors.Is(err, errReconnectTimer) {
				reconnecting = true
				retries = 0
				continue
			}
		}
		if isFatal(err) || retries >= s.cfg.MaxRetries {
			s.emit(Error{Err: err, Recoverable: false})
			return
		}
		s.emit(Error{Err: err, Recoverable: true})
		logger.WarnCF("gptlive", "connection failed, retrying", map[string]any{"error": err.Error(), "retry": retries + 1})
		retries++
		reconnecting = true
		select {
		case <-time.After(s.cfg.RetryInterval):
		case <-s.ctx.Done():
			return
		}
	}
}

// closeChannelsWhenIdle closes events and audio once nothing can ever push to
// either of them again, so a send from a feeder or an in-flight executeCall
// goroutine can never land on an already-closed channel (that send is a ready
// case in a select, and Go can pick it, which panics).
//
// audioQueue's only producer is the read goroutine, which run's own doc
// comment guarantees has already exited by the time this runs (runOnce only
// returns after stopReader has joined it) — so audioFeederStop can be closed
// immediately, with no need to wait on anything first, and the feeder
// goroutine (feedHandoff, given audioFeederStop) is asked to stop and drained
// via audioFeederDone before s.audio itself is closed below.
//
// eventQueue can still receive a push after that: an executeCall goroutine
// (delegation.go) calls emit(FunctionResult{...}) after its own tool finishes,
// which can be arbitrarily later than the read goroutine exiting. toolWG (Add
// in onResponseEvent, Done deferred in executeCall) is what tracks every one
// of those, so eventFeederStop is only closed — and only then can
// eventFeederDone be waited on — once toolWG reaches zero; only after that is
// it safe to close s.events.
//
// The toolWG wait is bounded by sessionCloseTimeout, the same bound Close
// already applies to waiting for session.closed, so a tool that ignores ctx
// cancellation and never returns cannot hang Close forever: run (and so
// Close's <-s.done) proceeds once that bound passes, and the channels
// themselves are only ever closed once toolWG actually reaches zero — that
// stalled call still cannot make emit panic later, since the close is simply
// finished in the background instead of inline.
func (s *Session) closeChannelsWhenIdle() {
	close(s.audioFeederStop)
	audioIdle := make(chan struct{})
	go func() {
		<-s.audioFeederDone
		close(audioIdle)
	}()

	idle := make(chan struct{})
	go func() {
		s.toolWG.Wait()
		close(s.eventFeederStop)
		<-s.eventFeederDone
		<-audioIdle
		close(idle)
	}()
	select {
	case <-idle:
		close(s.events)
		close(s.audio)
	case <-time.After(sessionCloseTimeout):
		logger.WarnCF("gptlive", "closing session while a tool call is still running; deferring channel close", nil)
		go func() {
			<-idle
			close(s.events)
			close(s.audio)
		}()
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
		// ROOT CAUSE (proven by a standalone probe against OpenAI, dev box: 4
		// configs, each ~44s, arrival ratio 0.988-1.001 with zero gaps >200ms
		// regardless of sample rate/delegation/prompt size, against 0.66 for
		// this worker at the same time): this used to block here — a
		// `select { case s.audio <- pcm: case <-s.ctx.Done(): }` straight into
		// the buffered channel Audio() exposes. Once a slow downstream
		// consumer let that buffer fill, THIS select stopped completing, which
		// stopped ReadMessage below from ever being called again, which is
		// what actually throttled the socket — TCP backpressure then made
		// OpenAI itself appear to slow down, when the real bottleneck was here
		// the whole time. audioQueue.push (handoff.go) never blocks no matter
		// how far behind the consumer is, so this can no longer stall the read
		// loop; a separate feeder goroutine (started in Dial) does the
		// blocking hand-off into s.audio instead, where it belongs.
		if ev.Delta == "" {
			return nil
		}
		pcm, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil || len(pcm) == 0 {
			return nil
		}
		if s.audioQueue != nil {
			s.audioQueue.push(pcm)
		} else {
			// Only reachable from a bare &Session{} a test built without Dial
			// (see the struct field's comment); every real, dialed session has
			// audioQueue set.
			select {
			case s.audio <- pcm:
			case <-s.ctx.Done():
			}
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
