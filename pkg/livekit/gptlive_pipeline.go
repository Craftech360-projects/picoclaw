package livekit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/media-sdk"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/sipeed/picoclaw/pkg/geminilive"
	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/grokvoice"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// GPTLiveSessionSpec is everything one RoomSession needs to run a GPT-Live
// session instead of the cascade AudioPipeline: the model connection details,
// the persona composed once at session start (Task 11), the tool registry and
// quiz tracker scoped to this session (Tasks 9-10), and the session's own
// sample rate (16000 for the device, 24000 for the browser demo — never
// resampled inside pkg/gptlive; lkmedia does any resampling at the track
// boundary).
type GPTLiveSessionSpec struct {
	APIKey             string
	Voice              any
	SampleRate         int
	Vendor             string // VendorOpenAI (""), VendorXAI or VendorGoogle
	Model              string // vendor model; "" = the vendor client's default
	BaseURL            string // vendor endpoint override; "" = the vendor client's default (tests point it at a fake)
	InRate             int    // mic sample rate sent to the model; 0 = SampleRate (Gemini needs 16000)
	BackendModel       string
	Persona            GPTLivePersona
	Tools              *tools.ToolRegistry
	Quiz               *QuizTracker
	WebSearch          bool
	MaxSessionDuration time.Duration
}

const (
	VendorOpenAI = "openai"
	VendorXAI    = "xai"
	VendorGoogle = "google"
)

// realtimeSession is what gptLivePipeline needs from a realtime voice vendor. *gptlive.Session,
// *grokvoice.Session and *geminilive.Session all satisfy it, and all speak gptlive events.
type realtimeSession interface {
	PushAudio(pcm []byte)
	Audio() <-chan []byte
	Events() <-chan gptlive.Event
	AppendInstructions(text string) // a standing rule (quiz Door directive)
	AppendCommentary(text string)   // something to say now (greeting, goodbye)
	Close(ctx context.Context) error
}

func (p *gptLivePipeline) inRate() int {
	if p.spec.InRate > 0 {
		return p.spec.InRate
	}
	if p.spec.Vendor == VendorGoogle {
		return geminilive.InputSampleRate // the Live API only accepts 16 kHz input
	}
	return p.spec.SampleRate
}

// defaultGPTLiveSampleRate mirrors gptlive.Config.withDefaults' own default.
// gptLivePipeline needs the same value the dialed session settles on (not just
// the session, which defaults internally): Start divides by p.spec.SampleRate
// on every audio frame, so a caller-supplied zero must be corrected here too,
// before it can ever reach that division.
const defaultGPTLiveSampleRate = 24000

// greetingFallbackDelay is how long Start waits for a "ready_for_greeting" data
// message before greeting on its own — mirroring the cascade's own fallback
// greeting behaviour so a lost/late control message never leaves the child
// sitting in silence.
const greetingFallbackDelay = 3 * time.Second

// farewellBurstCloseTimeout bounds how long handleGPTLiveEndPrompt waits for
// the model to finish speaking its goodbye before leaving anyway.
const farewellBurstCloseTimeout = 6 * time.Second

// gptLiveDirectiveSupersedes prefixes every Door directive pushed into the
// voice model's live instructions.
//
// The ADR sanctions AppendInstructions as the mechanism, and append is all the
// protocol offers — there is no replace and no remove. So over a ten-question
// session with misses the instructions accumulate 10-30 "## This Question"
// blocks, each naming a different question id, all of them still reading as
// current. The cascade never had this problem because it used per-turn system
// messages, which replace by construction.
//
// Two ways to make only the latest one authoritative. Re-sending a single
// consolidated block is the tidier model, but it does not actually help here:
// a consolidated block is still APPENDED, so the instructions grow at the same
// rate and every superseded block is still sitting above it verbatim. The
// prefix below is chosen instead because it is the only one that addresses the
// real failure — the model cannot tell which block is current — it costs one
// constant line per directive, it needs no extra state to rebuild, and it
// carries the rule at every insertion point rather than in one place a future
// caller can bypass.
const gptLiveDirectiveSupersedes = "IMPORTANT: this block REPLACES every earlier \"## This Question\" block in these instructions. " +
	"Those are finished questions; ignore all of them. Only the block below is current.\n\n"

// gptLivePipeline is the GPT-Live analogue of AudioPipeline: it owns the
// gptlive.Session for one room, pumps model audio out to the room's local
// track, pumps model events into transcript/state bookkeeping, and feeds room
// mic audio into the model. One instance per RoomSession, created
// synchronously in NewRoomSession (before Join ever runs — see the ready/
// startErr fields below for why), started from Join, and torn down from
// leave() — never a package-level var.
type gptLivePipeline struct {
	rs   *RoomSession
	spec GPTLiveSessionSpec
	sess realtimeSession
	// interrupts carries a vendor's barge-in from pumpEvents to driveSegmenter (buffered 1).
	interrupts chan struct{}
	seg        *gptlive.Segmenter
	// segmentEnded is set by the Segmenter's close callback (onSegmentEnd) and consumed by
	// driveSegmenter, which delays onBurstClose until the burst's queued playout has drained.
	// Only touched from the driveSegmenter goroutine (Feed/Tick run there).
	segmentEnded bool

	// mic-in flow counters for driveSegmenter's "audio flow" log (written by WriteSample)
	micSamples, micLast, micMaxGap, micMaxPush atomic.Int64

	micMu  sync.Mutex
	micBuf []byte // room mic at spec.SampleRate, drained by pumpMicIn

	// ready is closed once Start's call to gptlive.Dial has returned, whether
	// it succeeded or failed; startErr holds the result (nil on success) and
	// is only safe to read after <-ready. This is what lets handleTrackSubscribed
	// gate on rs.gptLiveSpec (known synchronously, before the room ever
	// connects) while still waiting for the pipeline to actually be dialed
	// before wiring a remote track to it, instead of racing Start's blocking
	// Dial (up to a 10s timeout and 3 retries at 2s).
	ready    chan struct{}
	startErr error

	// localTrack is captured once, in Start, from rs.localTrack (which Join
	// creates and publishes immediately before calling Start). Reading
	// rs.localTrack directly from pumpAudioOut later would race with leave()
	// clearing that field under rs.mu; this field is set exactly once, before
	// pumpAudioOut is spawned, and never mutated afterwards, so it needs no
	// lock of its own.
	localTrack *lkmedia.PCMLocalTrack

	// publish carries state/transcript publish work (SetAttributes, a reliable
	// data publish, or a text-stream SendText — all of which can block on
	// network I/O) off pumpAudioOut's goroutine, which must stay free to keep
	// draining model audio into the local track. pumpPublish is its sole
	// consumer. Buffered and non-blocking-send: a full queue drops the update
	// (logged) rather than ever stalling the caller, matching how
	// injectTurnEvent (room_session.go) already treats a full channel as
	// better dropped than blocked on.
	publish chan func()

	// burstClosed is signalled (non-blocking, capacity 1: only "has one closed
	// since I last checked" matters) every time onBurstClose runs, so
	// handleGPTLiveEndPrompt can wait for the farewell to actually finish
	// being spoken instead of guessing a fixed sleep.
	burstClosed chan struct{}

	// eventsDone is closed exactly once pumpEvents has returned for good (see
	// pumpEvents' defer), OR immediately by finishStart if pumpEvents is never
	// going to be spawned at all (the ctx-already-cancelled self-close branch,
	// or Start's own dial-error return). WaitForEventsDrain is the sole reader;
	// see its doc comment for why persistGPTLiveSession must wait on this
	// before reading VoiceSeconds/BackendTokens/TranscriptSnapshot.
	eventsDone chan struct{}

	mu sync.Mutex
	// eventsPumpStarted is true only once pumpEvents has actually been
	// spawned (finishStart's normal path). Guarded by mu like p.sess/p.cancel:
	// WaitForEventsDrain reads it from whichever goroutine calls Close/leave(),
	// finishStart writes it from its own goroutine.
	eventsPumpStarted bool
	// closing is set by Close before it asks the service to end the session, so
	// pumpEvents can tell a channel close WE caused from one the service caused
	// on its own (see leaveIfServiceEnded).
	closing bool
	state   string
	greeted bool
	// pendingGreet records a Greet() that arrived before the session was dialed
	// (see Greet and finishStart).
	pendingGreet bool
	// diagnostics for the "gptlive: greeting sent" and "gptlive: first model audio" lines
	startedAt        time.Time // Start was called (before the dial)
	greetedAt        time.Time
	firstAudioLogged bool
	agentText        map[string]string // burst id -> latest full text
	openAgentIDs     []string          // transcript ids seen since the burst opened
	transcript       []PersistedChatMessage
	voiceSeconds     float64
	backendTokens    int
	// backendInputTokens/backendOutputTokens are the Input/Output breakdown of
	// every summed BackendUsage event (backendTokens is their Total, which the
	// backend computes independently — see onEvent's BackendUsage case).
	// persistGPTLiveSession needs these specifically: sendUsageSummary
	// (post_session_persistence.go) treats InputTokens==0 && OutputTokens==0 as
	// "nothing to report" and skips the POST entirely, so reporting only
	// TotalTokens here would make every gptlive session's usage silently never
	// reach /device/token-usage (Task 13 review, Critical 1).
	backendInputTokens  int
	backendOutputTokens int
	cancel              context.CancelFunc
}

// newGPTLivePipeline builds the pipeline; call Start to dial the model and
// begin its pumps. It does no I/O and cannot block, so RoomSessionConfig.GPTLive
// callers construct it synchronously (NewRoomSession does, for exactly this
// reason — see the ready field's doc comment). state starts as "listening" to
// match the "lk.agent.state" attribute the room token already carries at mint
// time (generateRoomToken), so the first real transition published is a
// genuine speaking/listening edge rather than a synthetic bootstrap one.
func newGPTLivePipeline(rs *RoomSession, spec GPTLiveSessionSpec) *gptLivePipeline {
	p := &gptLivePipeline{
		rs: rs, spec: spec, state: "listening", agentText: map[string]string{},
		ready: make(chan struct{}), publish: make(chan func(), 64), burstClosed: make(chan struct{}, 1),
		eventsDone: make(chan struct{}), interrupts: make(chan struct{}, 1),
	}
	p.seg = gptlive.NewSegmenter(p.onBurstOpen, p.onSegmentEnd)
	if spec.Quiz != nil {
		// Score (agent_bridge-style contract documented on QuizTrackerConfig) calls
		// this synchronously, in-process, after releasing its own lock — never
		// concurrently with itself. In practice a quiz tool can only fire through
		// a function call the model made over an already-dialed p.sess, so this
		// read would be safe even unguarded — but Start writes p.sess without a
		// lock too (Task 12 review round 2, New Important 2), so this still reads
		// it under p.mu and copies it to a local before use, matching every other
		// reader of p.sess/p.cancel now.
		spec.Quiz.OnDirective(func(d string) {
			p.mu.Lock()
			sess := p.sess
			p.mu.Unlock()
			if sess != nil {
				sess.AppendInstructions(gptLiveDirectiveSupersedes + d)
			}
		})
	}
	return p
}

// pcm16ToBytes encodes PCM16 samples as little-endian bytes, the wire shape
// gptlive.Session.PushAudio and the model's own output both use.
func pcm16ToBytes(s media.PCM16Sample) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

// bytesToPCM16 is not redefined here: audio_pipeline.go already declares one
// package-wide (identical little-endian decoding, used by the cascade's own
// PCM assembler), and this pipeline reuses it rather than shadowing it.

// gptLiveTrackWriter adapts gptLivePipeline to lkmedia.PCMRemoteTrackWriter,
// which additionally requires a Close() error method. That must not be the
// pipeline's own Close(ctx): the remote track's lifecycle (closed whenever the
// participant disconnects, in leave()/handleParticipantDisconnected) is not
// this session's lifecycle — the gptlive.Session is only ever torn down once,
// from leave(), via gptLivePipeline.Close. This adapter's Close is therefore a
// deliberate no-op.
type gptLiveTrackWriter struct{ p *gptLivePipeline }

func (w gptLiveTrackWriter) WriteSample(sample media.PCM16Sample) error {
	return w.p.WriteSample(sample)
}
func (w gptLiveTrackWriter) Close() error { return nil }

// localOutTrackWriter is the minimal seam driveSegmenter needs onto the local
// (outbound, model-to-device) track: *lkmedia.PCMLocalTrack satisfies it in
// production, and TestDriveSegmenter* substitutes a fake in its place — the
// same reason driveSegmenter already takes audio/ticks as plain channels
// instead of reading p.sess.Audio() and its own ticker directly (see that
// function's doc comment). Kept separate from gptLiveTrackWriter above: that
// type adapts the pipeline itself to lkmedia.PCMRemoteTrackWriter for the
// opposite (device-to-model, inbound mic) direction and additionally needs a
// Close() method this seam has no use for.
type localOutTrackWriter interface {
	WriteSample(media.PCM16Sample) error
}

// Start dials GPT-Live with the persona and tools and begins the pumps that
// move audio and events between the model and the room.
//
// The pump goroutines run on a context derived from context.Background(), NOT
// from ctx (rs.ctx): leave() cancels rs.ctx immediately, before it calls
// Close, specifically so that other cleanup can proceed without waiting on
// the network — but pumpEvents must keep draining p.sess.Events() through
// Close's own sess.Close(ctx) call, or the final Closed{VoiceSeconds} event
// the service sends in response is emitted into a channel nothing is left to
// read (Task 12 review, Important 4). Close's own p.cancel() — called only
// after sess.Close returns — is what stops these goroutines. ctx itself is
// still respected for the dial: gptlive.Dial returns promptly if it is
// cancelled before the model answers.
//
// Because the pipeline object now exists from NewRoomSession (Task 12 review,
// Critical 1), leave() can run — and call Close — while this call is still
// blocked inside gptlive.Dial (up to ~16s with retries). If that happens,
// Close finds p.sess and p.cancel both still nil and does nothing; without
// the check right after they are assigned below (Task 12 review round 2, New
// Important 1), this function would go on to spawn four goroutines nothing
// could ever cancel — closeOnce has already fired, and leave() has already
// nil'd rs.gptlive, so no second Close call is coming. ctx is what leave()
// cancels first, before it ever calls Close, so checking ctx.Done() here is
// sufficient: if it's already closed, Close was already attempted (and
// failed silently) and this call must finish the job itself.
func (p *gptLivePipeline) Start(ctx context.Context) error {
	p.mu.Lock()
	p.startedAt = time.Now()
	p.mu.Unlock()
	if p.spec.SampleRate <= 0 {
		// gptlive.Config.withDefaults fills in 24000 inside the dialed Session,
		// but p.spec keeps whatever the caller passed — including zero — so
		// Dial would succeed while frameDur's later division by p.spec.SampleRate
		// panics on the very first audio frame. Corrected here, once, so every
		// later read of p.spec.SampleRate (the dial config, the remote track's
		// target rate, frameDur) agrees.
		p.spec.SampleRate = defaultGPTLiveSampleRate
	}
	p.localTrack = p.rs.localTrack

	sess, err := p.dial(ctx)
	if err != nil {
		p.startErr = err
		close(p.ready)
		// pumpEvents will never be spawned for this pipeline (Dial itself
		// failed), so nothing else will ever close eventsDone; do it here so
		// WaitForEventsDrain's caller (leave(), via persistGPTLiveSession)
		// never has to think about a dial failure differently from a normal
		// teardown — a bare no-op if this pipeline is later Close()'d.
		close(p.eventsDone)
		return err
	}
	return p.finishStart(ctx, sess)
}

// dial connects to the spec's vendor with the session's tools.
func (p *gptLivePipeline) dial(ctx context.Context) (realtimeSession, error) {
	var defs []map[string]any
	var exec gptlive.ToolExecutor
	if p.spec.Tools != nil {
		defs = GPTLiveToolDefs(p.spec.Tools, p.spec.WebSearch)
		exec = NewRegistryExecutor(p.spec.Tools, p.rs.roomName())
	}
	voice := ""
	if p.spec.Voice != nil {
		voice = fmt.Sprint(p.spec.Voice)
	}
	switch p.spec.Vendor {
	case VendorXAI:
		s, err := grokvoice.Dial(ctx, grokvoice.Config{
			APIKey: p.spec.APIKey, BaseURL: p.spec.BaseURL, Model: p.spec.Model, Voice: voice,
			Instructions: p.spec.Persona.Voice, Tools: defs, Executor: exec,
		})
		if err != nil {
			return nil, err // never a typed nil inside the interface
		}
		return s, nil
	case VendorGoogle:
		s, err := geminilive.Dial(ctx, geminilive.Config{
			APIKey: p.spec.APIKey, BaseURL: p.spec.BaseURL, Model: p.spec.Model, Voice: voice,
			Instructions: p.spec.Persona.Voice, Tools: defs, Executor: exec,
		})
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	s, err := gptlive.Dial(ctx, gptlive.Config{
		APIKey: p.spec.APIKey, BaseURL: p.spec.BaseURL, Model: p.spec.Model, Voice: p.spec.Voice, SampleRate: p.spec.SampleRate,
		Instructions: p.spec.Persona.Voice,
		Backend:      gptlive.ResponsesConfig{Model: p.spec.BackendModel, Instructions: p.spec.Persona.Backend, Tools: defs},
		Tools:        exec, MaxSessionDuration: p.spec.MaxSessionDuration,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// finishStart runs once Dial has returned a live session: it publishes
// p.sess/p.cancel/p.ready and either spawns the pumps or, if ctx is already
// cancelled, self-closes instead (see Start's doc comment for why). Split out
// from Start so this decision can be exercised directly in a test without a
// real dial (TestFinishStartSelfClosesWhenContextAlreadyCancelled).
func (p *gptLivePipeline) finishStart(ctx context.Context, sess realtimeSession) error {
	// pctx/cancel are created before the lock so the assignment below is a
	// single critical section; p.cancel is written here and nowhere else.
	pctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.sess = sess
	p.cancel = cancel
	p.mu.Unlock()

	select {
	case <-ctx.Done():
		// leave() already ran (it cancels rs.ctx, which is what was passed as
		// ctx here, before it ever calls Close) while Dial was still in
		// flight. Its Close call found p.sess/p.cancel nil and did nothing;
		// finish that close now, since nothing else will ever call it again.
		//
		// startErr must be set to a non-nil error BEFORE close(p.ready) here,
		// not after (Task 12 review carried forward to Task 13): a
		// handleTrackSubscribed goroutine can be blocked on <-p.ready right
		// now, and once that receive unblocks it immediately reads p.startErr
		// with no further synchronization. Closing ready first would let it
		// observe nil — the zero value, meaning "dialed fine" — and go on to
		// wire the room's mic track to a pipeline that is, in this very branch,
		// about to be torn down. Setting startErr first (the same
		// write-before-close pattern Start's own dial-error path above already
		// uses) makes the waiter bail deterministically instead.
		p.startErr = ctx.Err()
		close(p.ready)
		// Nothing below ever spawns pumpEvents on this path; see eventsDone's
		// own doc comment for why this must still close it.
		close(p.eventsDone)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		p.Close(closeCtx)
		closeCancel()
		return ctx.Err()
	default:
		close(p.ready)
	}

	p.mu.Lock()
	p.eventsPumpStarted = true
	p.mu.Unlock()
	go p.pumpAudioOut(pctx)
	go p.pumpMicIn(pctx)
	go p.pumpEvents(pctx)
	go p.pumpPublish(pctx)
	go func() {
		select {
		case <-time.After(greetingFallbackDelay):
			p.greet("fallback_timer")
		case <-pctx.Done():
		}
	}()
	// A "ready_for_greeting" that landed while Dial was still in flight was
	// recorded rather than acted on (see Greet); honour it now, so the child
	// does not wait out the fallback timer above for a greeting the gateway
	// already asked for. Greet is idempotent, so this racing the timer is fine.
	p.mu.Lock()
	pendingGreet := p.pendingGreet
	p.mu.Unlock()
	if pendingGreet {
		p.greet("ready_for_greeting_before_dial")
	}
	return nil
}

// WriteSample is the PCMRemoteTrack writer for room mic audio: it goes
// straight to the model at the session's own sample rate. lkmedia has already
// resampled/downmixed the room track to that rate before this is called, so
// pkg/gptlive itself never resamples anything.
//
// Reads p.sess without p.mu, unlike Close/Greet/SayGoodbyeAndWait/the quiz
// OnDirective closure (Task 12 review round 2, New Important 2): this is only
// ever called from the PCMRemoteTrack processing goroutine that
// lkmedia.NewPCMRemoteTrack spawns, which handleTrackSubscribed only creates
// after receiving on p.ready — a channel receive that happens-after Start's
// write to p.sess (which happens before close(p.ready)). That happens-before
// chain is what the other callers below don't have: they can run before
// Start ever gets there (Greet/SayGoodbyeAndWait from a data message, the
// quiz closure from a tool call — none of which wait on p.ready).
func (p *gptLivePipeline) WriteSample(sample media.PCM16Sample) error {
	now := time.Now().UnixNano()
	if last := p.micLast.Swap(now); last != 0 && now-last > p.micMaxGap.Load() {
		p.micMaxGap.Store(now - last) // one writer goroutine, so load-then-store is fine
	}
	p.micSamples.Add(int64(len(sample)))
	p.micMu.Lock()
	p.micBuf = append(p.micBuf, pcm16ToBytes(sample)...)
	// ponytail: 500ms cap so a stalled pump can't grow input latency; drops the oldest audio
	if limit := p.inRate(); len(p.micBuf) > limit {
		p.micBuf = append(p.micBuf[:0], p.micBuf[len(p.micBuf)-limit:]...)
	}
	p.micMu.Unlock()
	return nil
}

// pumpMicIn sends the mic to GPT-Live the way the livekit-agents plugin does: fixed 100ms
// chunks (AudioByteStream, SAMPLE_RATE // 10) on a steady clock. The model is clocked by its
// input, so network-shaped mic delivery became network-shaped model output. Until 200ms is
// buffered, and whenever the mic falls short, it sends silence to keep that clock steady.
func (p *gptLivePipeline) pumpMicIn(ctx context.Context) {
	chunk := p.inRate() / 10 * 2 // 100ms of PCM16 mono
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	primed := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		out := make([]byte, chunk)
		p.micMu.Lock()
		if !primed && len(p.micBuf) >= 2*chunk {
			primed = true
		}
		if primed {
			n := copy(out, p.micBuf)
			p.micBuf = append(p.micBuf[:0], p.micBuf[n:]...)
			if n < chunk {
				primed = false // ran dry: rebuild the 100ms of slack before taking mic again
			}
		}
		p.micMu.Unlock()
		start := time.Now()
		p.sess.PushAudio(out)
		if d := time.Since(start).Nanoseconds(); d > p.micMaxPush.Load() {
			p.micMaxPush.Store(d) // PushAudio can block up to 1s on a full send queue
		}
	}
}

// Greet asks the voice model to speak the character's greeting immediately.
// Idempotent: only the first caller (the "ready_for_greeting" data message or
// the fallback timer, whichever comes first) has any effect.
//
// A call that lands before the session is dialed is REMEMBERED, not dropped.
// Join connects the room and publishes the local track before it calls Start,
// so the gateway's "ready_for_greeting" can arrive across the whole dial window
// (a 10s timeout with 3 retries at 2s). Returning silently in that window left
// the child waiting on the 3-second fallback timer, which does not even start
// until finishStart has spawned it — so the greeting could be seconds late for
// no reason. finishStart replays the flag the moment the session exists.
//
// This sends only a short nudge, NOT the greeting prompt itself: the greeting
// prompt (manager-supplied, arbitrarily long) already lives in Persona.Voice's
// <greeting_guidance> section, sent once at session start over a channel with
// no length cap. A real session logged "gptlive: invalid_request_error
// (invalid_value): Context append text must not exceed 500 tokens" and the
// child was never greeted, because this used to append the full guidance as
// commentary — a channel the service caps per-append. A short, fixed nudge is
// comfortably under that cap regardless of how long any character's greeting
// guidance is.
func (p *gptLivePipeline) Greet() { p.greet("ready_for_greeting") }

// greet is Greet with the trigger named for the "gptlive: greeting sent" log line.
func (p *gptLivePipeline) greet(source string) {
	p.mu.Lock()
	if p.greeted {
		p.mu.Unlock()
		return
	}
	if p.sess == nil {
		p.pendingGreet = true
		p.mu.Unlock()
		return
	}
	p.greeted = true
	p.greetedAt = time.Now()
	sess := p.sess
	sinceStart := msSinceOrNeg(p.startedAt)
	p.mu.Unlock()
	logger.InfoCF("livekit", "gptlive: greeting sent", map[string]any{
		"room": p.rs.roomName(), "vendor": p.spec.Vendor, "greet_source": source, "ms_since_start": sinceStart,
	})
	sess.AppendCommentary("Greet the child now, following your greeting guidance from the session instructions. Do not wait for them to speak first. After that, pause and listen.")
}

// SayGoodbyeAndWait is the GPT-Live equivalent of the cascade's farewell:
// there is no separate TTS pipeline to hand a fixed sentence to, so the model
// is asked to speak its own goodbye, and the call waits for the resulting
// burst to close (the segmenter's own signal that the model stopped talking)
// rather than guessing a fixed duration — bounded by timeout and ctx so a
// disconnect or a model that never finishes cannot hang the caller.
//
// Draining any stale, already-pending burstClosed signal first matters: without
// it, a burst that closed for an unrelated reason moments before this was
// called would be consumed immediately, returning before the farewell had
// even started.
func (p *gptLivePipeline) SayGoodbyeAndWait(ctx context.Context, prompt string, timeout time.Duration) {
	select {
	case <-p.burstClosed:
	default:
	}
	p.mu.Lock()
	sess := p.sess
	p.mu.Unlock()
	if sess == nil {
		return
	}
	sess.AppendCommentary("Say a short, warm goodbye to the child now, along these lines, then stop talking: " + prompt)
	select {
	case <-p.burstClosed:
	case <-time.After(timeout):
	case <-ctx.Done():
	}
}

// pumpAudioOut drains model audio to the local track, on a 100ms ticker that
// drives the Segmenter alongside it. It is a thin wrapper around
// driveSegmenter (below) that supplies the real session's audio channel and a
// real ticker; driveSegmenter carries the actual logic so a test can drive it
// with fake channels instead of a dialed gptlive.Session.
func (p *gptLivePipeline) pumpAudioOut(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	// p.localTrack is a concrete *lkmedia.PCMLocalTrack; assigning it into the
	// localOutTrackWriter interface variable only when non-nil avoids the
	// classic typed-nil trap (a nil *PCMLocalTrack boxed into a non-nil
	// interface would make driveSegmenter's own `track != nil` check true and
	// then panic on the call) — the interface stays a true nil when there is
	// no track, exactly like p.localTrack itself.
	var track localOutTrackWriter
	if p.localTrack != nil {
		track = p.localTrack
	}
	p.driveSegmenter(ctx, p.sess.Audio(), ticker.C, track)
}

// elasticPlayoutTarget is the lead driveSegmenter holds in the local track. GPT-Live
// delivers at real time (0.96-1.02 measured), so a lead only refills if we add it:
// silence frames are stretched when the lead is short and dropped when it is long,
// and speech is never held or dropped. livekit-agents' room output uses 200ms too.
// docs/gptlive-audio-jitter.md has the measurements.
const elasticPlayoutTarget = 200 * time.Millisecond

// driveSegmenter is the single loop that drains model audio into the local track
// and drives the Segmenter. segment.go requires Feed and Tick to be serialized;
// reading the audio channel and the ticker in one select makes that structural.
// The channels and track are parameters so tests can drive it without a dialed
// session or a real *lkmedia.PCMLocalTrack.
//
// Playout is elastic (see elasticPlayoutTarget), and every write is cut to whole
// 20ms frames so PCMLocalTrack never zero-pads a partial frame mid-speech. Feed
// happens at arrival: the gate must classify a chunk before we decide whether it
// is silence we may stretch or drop.
func (p *gptLivePipeline) driveSegmenter(ctx context.Context, audio <-chan []byte, ticks <-chan time.Time, track localOutTrackWriter) {
	frameDur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(p.spec.SampleRate) }
	frameBytes := p.spec.SampleRate / 50 * 2
	var carry []byte
	var level, underrun time.Duration // level mirrors the track's queue: +write, -wall time
	var levelAt time.Time
	underruns, writeErrs := 0, 0
	starved := false
	// leave() closes the local track before this loop stops, so every frame still
	// queued fails to write: log the first, count the rest, report once on exit.
	defer func() {
		if writeErrs > 1 {
			logger.WarnCF("livekit", "gptlive: further writes to the local track failed", map[string]any{
				"room":       p.rs.roomName(),
				"suppressed": writeErrs - 1,
			})
		}
	}()

	const flowEvery = 5 * time.Second
	windowStart, lastArrival := time.Now(), time.Time{}
	var outArrived, outMaxGap time.Duration

	drain := func(now time.Time) {
		if !levelAt.IsZero() {
			level -= now.Sub(levelAt)
		}
		levelAt = now
		if level < 0 {
			if p.seg.Open() {
				underrun -= level
				if !starved {
					underruns++
				}
			}
			starved = true
			level = 0
		}
	}
	logFlow := func(now time.Time) {
		elapsed := now.Sub(windowStart)
		if elapsed < flowEvery {
			return
		}
		micSamples := p.micSamples.Swap(0)
		logger.InfoCF("livekit", "gptlive: audio flow", map[string]any{
			"room":           p.rs.roomName(),
			"in_ratio":       float64(micSamples) / (float64(p.inRate()) * elapsed.Seconds()),
			"in_max_gap_ms":  time.Duration(p.micMaxGap.Swap(0)).Milliseconds(),
			"push_max_ms":    time.Duration(p.micMaxPush.Swap(0)).Milliseconds(),
			"out_ratio":      outArrived.Seconds() / elapsed.Seconds(),
			"out_max_gap_ms": outMaxGap.Milliseconds(),
			"underruns":      underruns,
			"underrun_ms":    underrun.Milliseconds(),
			"level_ms":       level.Milliseconds(),
			"speaking":       p.seg.Open(),
		})
		windowStart, outArrived, outMaxGap, underruns, underrun = now, 0, 0, 0, 0
	}
	write := func(pcm []byte) {
		if len(pcm) == 0 || track == nil {
			return
		}
		level += frameDur(len(pcm))
		starved = false
		if err := track.WriteSample(bytesToPCM16(pcm)); err != nil {
			if writeErrs++; writeErrs == 1 {
				logger.WarnCF("livekit", "gptlive: write to local track", map[string]any{"error": err.Error()})
			}
		}
	}
	// burstEnd is when the last closed burst's queued audio finishes playing out (zero: none
	// pending). level at the segment close is exactly that audio: the chunk that closed it has
	// not been written yet. A real-time source (GPT-Live) leaves at most the ~200ms elastic lead
	// queued, a faster one (Gemini) seconds.
	var burstEnd time.Time
	settleBurst := func(now time.Time) {
		if p.segmentEnded {
			p.segmentEnded = false
			burstEnd = now.Add(level)
		}
		if p.seg.Open() {
			burstEnd = time.Time{} // speech resumed before playout drained: still one burst
		}
		if !burstEnd.IsZero() && !now.Before(burstEnd) {
			burstEnd = time.Time{}
			p.onBurstClose()
		}
	}
	flushBurst := func() { // teardown: never lose the pending burst's transcript
		if !burstEnd.IsZero() || p.segmentEnded {
			burstEnd, p.segmentEnded = time.Time{}, false
			p.onBurstClose()
		}
	}

	for {
		select {
		case pcm, ok := <-audio:
			if !ok {
				write(carry)
				flushBurst()
				return
			}
			now := time.Now()
			drain(now)
			if !lastArrival.IsZero() && now.Sub(lastArrival) > outMaxGap {
				outMaxGap = now.Sub(lastArrival)
			}
			lastArrival = now
			outArrived += frameDur(len(pcm))
			p.seg.Feed(pcm, frameDur(len(pcm)))
			settleBurst(now)
			carry = append(carry, pcm...)
			n := len(carry) / frameBytes * frameBytes
			if n == 0 {
				continue
			}
			out := carry[:n]
			if !p.seg.Open() {
				switch {
				case level > 2*elasticPlayoutTarget:
					out = nil
				case level < elasticPlayoutTarget:
					pad := int((elasticPlayoutTarget-level)/(20*time.Millisecond)) * frameBytes
					out = append(make([]byte, pad, pad+n), out...)
				}
			}
			write(out) // bytesToPCM16 copies, so reusing carry below is safe
			carry = append(carry[:0], carry[n:]...)
		case <-p.interrupts: // nil channel on GPT-Live-only test pipelines: never ready
			if c, ok := track.(interface{ ClearQueue() }); ok {
				c.ClearQueue()
			}
			carry, level, starved = carry[:0], 0, true
			if !burstEnd.IsZero() {
				burstEnd = time.Now() // the queued audio is gone: end the burst now
			}
			settleBurst(time.Now())
		case now := <-ticks:
			drain(now)
			p.seg.Tick(now)
			settleBurst(now)
			logFlow(now)
		case <-ctx.Done():
			write(carry) // the last partial frame, so teardown never abandons audio
			flushBurst()
			return
		}
	}
}

// pumpPublish is the sole executor of state/transcript publish work
// (setState, publishTranscript), kept off pumpAudioOut so a slow SetAttributes/
// PublishAgentState/SendText round trip can never stall audio draining.
func (p *gptLivePipeline) pumpPublish(ctx context.Context) {
	for {
		select {
		case fn := <-p.publish:
			fn()
		case <-ctx.Done():
			return
		}
	}
}

// enqueuePublish hands one unit of publish work to pumpPublish. Non-blocking:
// a full queue drops the update (logged) rather than risking the caller —
// onBurstOpen/onBurstClose, called synchronously from pumpAudioOut — ever
// blocking on it.
func (p *gptLivePipeline) enqueuePublish(fn func()) {
	if p.publish == nil {
		// A bare &gptLivePipeline{} (what the pure-logic unit tests construct,
		// bypassing newGPTLivePipeline, to exercise onEvent/onBurstClose without
		// dialing a real session) has no queue and no pumpPublish to drain it;
		// there is nothing to warn about here.
		return
	}
	select {
	case p.publish <- fn:
	default:
		logger.WarnCF("livekit", "gptlive: publish queue full; dropping a state/transcript update", map[string]any{"room": p.rs.roomName()})
	}
}

func (p *gptLivePipeline) pumpEvents(ctx context.Context) {
	// Closed on every return path (including via drainEvents below) so
	// WaitForEventsDrain can tell pumpEvents has actually stopped touching
	// p.voiceSeconds/p.backendTokens/p.transcript, rather than inferring it
	// from Close having returned.
	defer close(p.eventsDone)
	for {
		select {
		case ev, ok := <-p.sess.Events():
			if !ok {
				p.leaveIfServiceEnded()
				return
			}
			p.onEvent(ev)
		case <-ctx.Done():
			// ctx (pctx) is only ever cancelled from Close, and only after
			// sess.Close(ctx) has already returned (see Close's doc comment) —
			// which per gptlive.Session.run/closeChannelsWhenIdle means
			// p.sess.Events() is already closed by now. Closing a channel does
			// NOT discard whatever was still buffered in it (in particular the
			// final Closed{VoiceSeconds} event, sent just before the channel was
			// closed): a plain `return` here, with the ctx.Done() case winning a
			// select against an equally-ready buffered-event case at random
			// (Task 12/13 review: "carried forward, not fixed"), could leave that
			// event unread forever. Drain synchronously instead — see
			// drainEvents's own doc comment for why this cannot block.
			drainEvents(p.sess.Events(), p.onEvent)
			return
		}
	}
}

// leaveIfServiceEnded tears the room down when p.sess.Events() closed without
// this side having asked for it.
//
// onEvent's Error{Recoverable:false} case is NOT enough on its own: run()
// returns nil, emitting no Error at all, whenever classify sees closedEv
// already closed (gptlive/session.go) — which is exactly the path taken when
// the SERVICE ends the session by itself: its own duration cap, a quota trip,
// an operator action. run then closes the event and audio channels, pumpEvents
// and pumpAudioOut observe that and return, and before this existed nothing
// published a state change, disconnected the room or persisted anything. The
// child was left in a connected room with an agent that would never speak
// again.
//
// p.closing (set by Close, before it asks the service to end the session) is
// the discriminator: a channel close we caused is an ordinary teardown that
// leave() is already driving, and calling Leave from here would be re-entrant.
// Leave itself is closeOnce-guarded and nil-safe, so the redundant call after
// an unrecoverable Error (which already scheduled one) is harmless.
func (p *gptLivePipeline) leaveIfServiceEnded() {
	if !p.shouldLeaveOnEventsClose() {
		return
	}
	logger.WarnCF("livekit", "gptlive: session ended by the service; leaving the room", map[string]any{
		"room": p.rs.roomName(),
	})
	p.setState("listening")
	go p.rs.Leave()
}

// shouldLeaveOnEventsClose is leaveIfServiceEnded's decision, split out so it
// can be exercised without a dialed session.
func (p *gptLivePipeline) shouldLeaveOnEventsClose() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closing
}

// drainEvents receives from events until it reports closed, calling onEvent
// for each value first. Split out from pumpEvents' ctx.Done() case
// (Task 13 review, Important 2/3) so the "a closed channel still yields
// whatever was buffered before it closed, in order, with no blocking" claim
// pumpEvents relies on can be exercised directly with a synthetic channel
// (TestDrainEventsProcessesBufferedEventsBeforeReturning) instead of only
// through a real dial + fake server race that may or may not land the timing
// window on a given run.
func drainEvents(events <-chan gptlive.Event, onEvent func(gptlive.Event)) {
	for {
		ev, ok := <-events
		if !ok {
			return
		}
		onEvent(ev)
	}
}

func (p *gptLivePipeline) onEvent(ev gptlive.Event) {
	switch e := ev.(type) {
	case gptlive.UserTranscript:
		// Only the final transcript is published (Task 12 review, Important 1):
		// gptlive emits one UserTranscript per incoming delta, and the gateway's
		// 5-second SendText dedup never fires because the cumulative text differs
		// on every call, so publishing every interim delta turns one utterance
		// into roughly as many MQTT messages as it has words.
		if e.Final {
			p.publishTranscript(e.ID, e.Text, true, true)
			p.mu.Lock()
			p.transcript = append(p.transcript, PersistedChatMessage{ChatType: chatTypeUser, Content: e.Text, Timestamp: time.Now().UnixMilli()})
			p.mu.Unlock()
		}
	case gptlive.AgentTranscript:
		// Bookkeeping only here; the one publish for this burst happens in
		// onBurstClose once the whole utterance is assembled (same Important 1
		// fix as above — this case used to publish on every delta too).
		p.mu.Lock()
		if p.agentText == nil {
			// Lazily initialized rather than relying solely on newGPTLivePipeline:
			// a pipeline built as a bare &gptLivePipeline{} (as the pure-logic unit
			// tests do, to exercise onEvent/onBurstClose without dialing a real
			// session) must not panic on a nil map write.
			p.agentText = map[string]string{}
		}
		if _, seen := p.agentText[e.ID]; !seen {
			p.openAgentIDs = append(p.openAgentIDs, e.ID)
		}
		p.agentText[e.ID] = e.Text
		p.mu.Unlock()
	case gptlive.DelegationStarted:
		// Deliberately not a state transition: the ADR makes the segmenter the
		// one boundary for agent state (speaking on burst open, listening on
		// burst close). A delegation can complete without the model ever
		// producing more audio, so treating it as "thinking" would risk leaving
		// the published state stuck there. Logged for observability only.
		logger.DebugCF("livekit", "gptlive: delegation started", map[string]any{"id": e.ID, "room": p.rs.roomName()})
	case gptlive.FunctionCall:
		logger.InfoCF("livekit", "gptlive: tool call", map[string]any{"name": e.Name, "room": p.rs.roomName()})
	case gptlive.FunctionResult:
		if e.IsError {
			logger.WarnCF("livekit", "gptlive: tool error", map[string]any{"name": e.Name, "output": e.Output})
		}
	case gptlive.Interrupted:
		select {
		case p.interrupts <- struct{}{}:
		default: // one pending barge-in is enough
		}
	case gptlive.VoiceUsage:
		// Seconds is the session's running total to date, not a delta since the
		// last update, so the latest value replaces rather than accumulates —
		// but only upwards. See recordVoiceSeconds.
		p.recordVoiceSeconds(e.Seconds)
	case gptlive.BackendUsage:
		// Total/Input/Output are all per backend response (one per delegation),
		// so summing each across every response completed this session gives the
		// session totals. Input/Output are tracked alongside Total (not just
		// derived from it) because persistGPTLiveSession needs them specifically:
		// sendUsageSummary skips its POST entirely when both are zero (Task 13
		// review, Critical 1).
		p.mu.Lock()
		p.backendTokens += e.Total
		p.backendInputTokens += e.Input
		p.backendOutputTokens += e.Output
		p.mu.Unlock()
	case gptlive.Closed:
		// Monotonic, NOT an unconditional assignment: gptlive.Session's
		// handleEvent sets VoiceSeconds to 0 whenever session.closed arrives
		// with no usage object at all (ServerEvent.Usage is a pointer), and
		// leave() is deliberately ordered Close -> WaitForEventsDrain -> persist
		// so this is the LAST write before persistGPTLiveSession reads it.
		// Overwriting here with a zero would post sessionDurationSeconds 0 for
		// every session, silently and forever.
		p.recordVoiceSeconds(e.VoiceSeconds)
	case gptlive.Error:
		logger.WarnCF("livekit", "gptlive: session error", map[string]any{"error": e.Err.Error(), "recoverable": e.Recoverable})
		if !e.Recoverable {
			// The session has given up permanently (MaxRetries exhausted, or a
			// fatal protocol error) and will never reconnect on its own. Leaving
			// the child in a room that still shows the agent as connected, with no
			// way to hear or be heard, is worse than ending the call: reset to a
			// neutral published state and tear the whole room session down, the
			// same way a cascade session ends when its own STT/LLM/TTS chain dies
			// unrecoverably.
			p.setState("listening")
			go p.rs.Leave()
		}
	}
}

// recordVoiceSeconds is the single writer of p.voiceSeconds, and it only ever
// accepts a LARGER value. Both events that carry a voice-seconds figure —
// VoiceUsage (the running total to date) and Closed (the service's final
// report) — are cumulative session totals, so a smaller number can only be a
// counter that lost history, never real billing going backwards. Two ways that
// happens, both of which used to silently destroy the session's usage:
//
//   - session.closed with no usage object. gptlive.Session's handleEvent then
//     emits Closed{VoiceSeconds: 0}, and this is the last write leave() sees.
//   - a reconnect. gptlive.Session.run sets `reconnecting` on ANY recoverable
//     failure and resetForReconnect starts a fresh server session, so the first
//     usage.updated after a blip reports that new session's seconds, not the
//     call's. Keeping the high-water mark under-reports the post-blip audio;
//     accepting the reset would throw away everything before it. Neither this
//     repo nor the wire protocol can currently confirm which the service does
//     (our own fake server hard-codes a constant), so this takes the bounded
//     error over the unbounded one.
func (p *gptLivePipeline) recordVoiceSeconds(secs float64) {
	p.mu.Lock()
	if secs > p.voiceSeconds {
		p.voiceSeconds = secs
	}
	p.mu.Unlock()
}

func (p *gptLivePipeline) onBurstOpen() {
	p.mu.Lock()
	first := !p.firstAudioLogged
	p.firstAudioLogged = true
	sinceStart, sinceGreet := msSinceOrNeg(p.startedAt), msSinceOrNeg(p.greetedAt)
	p.mu.Unlock()
	if first {
		logger.InfoCF("livekit", "gptlive: first model audio", map[string]any{
			"room": p.rs.roomName(), "vendor": p.spec.Vendor, "ms_since_start": sinceStart, "ms_since_greet": sinceGreet,
		})
	}
	p.setState("speaking")
}

// msSinceOrNeg is milliseconds since t, or -1 when t was never set.
func msSinceOrNeg(t time.Time) int64 {
	if t.IsZero() {
		return -1
	}
	return time.Since(t).Milliseconds()
}

// onSegmentEnd is the Segmenter's close callback: the model stopped SENDING speech. The burst
// only ends (onBurstClose) once driveSegmenter sees that speech finish PLAYING: Gemini streams
// a turn faster than real time, so seconds can still be queued in the local track.
func (p *gptLivePipeline) onSegmentEnd() { p.segmentEnded = true }

// onBurstClose finalises the agent turn: publish the final transcript, record
// it, and wake anyone waiting on burstClosed (handleGPTLiveEndPrompt).
func (p *gptLivePipeline) onBurstClose() {
	p.mu.Lock()
	var parts []string
	for _, id := range p.openAgentIDs {
		if t := strings.TrimSpace(p.agentText[id]); t != "" {
			parts = append(parts, t)
		}
		delete(p.agentText, id)
	}
	ids := p.openAgentIDs
	p.openAgentIDs = nil
	text := strings.Join(parts, " ")
	if text != "" {
		p.transcript = append(p.transcript, PersistedChatMessage{ChatType: chatTypeAssistant, Content: text, Timestamp: time.Now().UnixMilli()})
	}
	p.mu.Unlock()
	if len(ids) > 0 && text != "" {
		p.publishTranscript(ids[len(ids)-1], text, true, false)
	}
	p.setState("listening")
	select {
	case p.burstClosed <- struct{}{}:
	default:
	}
}

// setState is the only place that changes p.state and publishes it, called
// exclusively from the segmenter's onBurstOpen/onBurstClose callbacks (plus
// the initial value set in newGPTLivePipeline) and from the unrecoverable-error
// path in onEvent — the ADR's "segmenter is the one boundary" for agent state
// during normal operation. The actual publish is hop over to pumpPublish
// (enqueuePublish) rather than done inline: this runs on pumpAudioOut's
// goroutine, which must stay free to keep draining model audio (Task 12
// review, Important 2).
func (p *gptLivePipeline) setState(next string) {
	p.mu.Lock()
	prev := p.state
	p.state = next
	p.mu.Unlock()
	if prev == next {
		return
	}
	p.enqueuePublish(func() {
		room := p.rs.roomSnapshot()
		if room == nil {
			return
		}
		room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": next})
		// Not p.rs.PublishAgentState(prev, next): that would re-read rs.room
		// rather than use the room already snapshotted above, and leave() clears
		// that field concurrently — the very nil-check-then-use race this
		// closure, running on the long-lived pumpPublish goroutine, must not
		// lose (Task 12 review round 2, New Important 3).
		// publishAgentStateOnRoom is now the single implementation of this
		// publish; RoomSession.PublishAgentState is a one-line wrapper over it
		// (room_session.go), so a future change to the payload can no longer
		// reach gptlive sessions and silently miss cascade ones.
		_ = publishAgentStateOnRoom(room, prev, next)
	})
}

// publishAgentStateOnRoom is THE "agent_state_changed" data publish, for both
// pipelines: it takes the room as a parameter rather than reading rs.room, so
// gptLivePipeline.setState can publish against a room it has already
// snapshotted (see the comment there), and RoomSession.PublishAgentState is a
// one-line wrapper over it for every cascade caller.
func publishAgentStateOnRoom(room *lksdk.Room, oldState, newState string) error {
	if room == nil || room.LocalParticipant == nil {
		return errors.New("room local participant not ready")
	}
	payload := map[string]any{
		"type": "agent_state_changed",
		"data": map[string]string{"old_state": oldState, "new_state": newState},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))
}

// publishTranscript mirrors the cascade's lk.transcription text-stream shape
// (lk.segment_id, lk.final, and lk.transcribed_track_id for the child's own
// speech) so the dashboard/gateway need no changes to understand a GPT-Live
// session's transcripts. Like setState, the actual SendText is handed to
// pumpPublish rather than done inline.
func (p *gptLivePipeline) publishTranscript(segmentID, text string, final, user bool) {
	if text == "" {
		return
	}
	trackSID := ""
	if user {
		trackSID = p.rs.remoteAudioTrackSID()
	}
	p.enqueuePublish(func() {
		// room is captured once, under rs.mu, rather than read from p.rs.room
		// twice (a nil check, then a use) — leave() clears that field under the
		// same lock, and the two-read version could observe non-nil on the check
		// and nil by the use, panicking in this pump goroutine and taking down
		// the whole worker (Task 12 review, Critical 3).
		room := p.rs.roomSnapshot()
		if room == nil {
			return
		}
		attrs := map[string]string{"lk.segment_id": segmentID, "lk.final": map[bool]string{true: "true", false: "false"}[final]}
		if user {
			attrs["lk.transcribed_track_id"] = trackSID
		}
		_ = room.LocalParticipant.SendText(text, lksdk.StreamTextOptions{Topic: "lk.transcription", Attributes: attrs})
	})
}

func (p *gptLivePipeline) TranscriptSnapshot() []PersistedChatMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PersistedChatMessage(nil), p.transcript...)
}

func (p *gptLivePipeline) VoiceSeconds() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.voiceSeconds
}
func (p *gptLivePipeline) BackendTokens() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backendTokens
}

// BackendInputTokens/BackendOutputTokens are the Input/Output breakdown behind
// BackendTokens' Total (see onEvent's BackendUsage case). persistGPTLiveSession
// needs these specifically, not just the total: sendUsageSummary's own guard
// skips the POST entirely when both are zero.
func (p *gptLivePipeline) BackendInputTokens() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backendInputTokens
}

func (p *gptLivePipeline) BackendOutputTokens() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backendOutputTokens
}

// WaitForEventsDrain blocks until pumpEvents has actually returned (eventsDone
// closed) or timeout elapses, whichever comes first — bounded so a caller (only
// leave(), today) can never hang: pumpEvents is expected to exit within
// microseconds of Close's cancel() firing, so timeout is only a backstop, not
// the normal path.
//
// Safe to call even when pumpEvents was never spawned at all — a dial failure
// in Start, or finishStart's own ctx-already-cancelled self-close branch — by
// checking eventsPumpStarted first: in both of those cases eventsDone is
// already closed too (see their own comments), so this would actually return
// immediately either way, but checking the flag avoids relying on that and
// documents the no-pump case explicitly.
func (p *gptLivePipeline) WaitForEventsDrain(timeout time.Duration) {
	p.mu.Lock()
	started := p.eventsPumpStarted
	p.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-p.eventsDone:
	case <-time.After(timeout):
	}
}

// Close tears down the underlying gptlive.Session — sess.Close waits for the
// service's final usage report (the Closed{VoiceSeconds} event; see the note
// on Start about why the pump goroutines stay alive for this) bounded by ctx —
// and only then cancels the pump goroutines Start spawned. Called from
// leave() before persistGPTLiveSession reads VoiceSeconds/BackendTokens/
// TranscriptSnapshot (Task 12 review, Important 4: persisting before Close
// would read a stale voiceSeconds, since Close is what makes the final update
// arrive at all) — and, once more, from finishStart's own self-close path if
// leave()'s call found p.sess/p.cancel still nil (Task 12 review round 2, New
// Important 1). Reads p.sess/p.cancel under p.mu rather than directly (New
// Important 2): Start(/finishStart) writes them from a different goroutine
// than whichever one calls Close.
func (p *gptLivePipeline) Close(ctx context.Context) {
	p.mu.Lock()
	sess, cancel := p.sess, p.cancel
	// Set before sess.Close, so pumpEvents can never see the resulting channel
	// close and mistake it for a service-initiated end (leaveIfServiceEnded).
	p.closing = true
	p.mu.Unlock()
	if sess != nil {
		_ = sess.Close(ctx)
	}
	if cancel != nil {
		cancel()
	}
}
