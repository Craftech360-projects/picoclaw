package livekit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/livekit/media-sdk"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/sipeed/picoclaw/pkg/gptlive"
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
	BackendModel       string
	Persona            GPTLivePersona
	Tools              *tools.ToolRegistry
	Quiz               *QuizTracker
	WebSearch          bool
	MaxSessionDuration time.Duration
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

// audioLeadBuffer is the playout lead driveSegmenter holds ahead of the local
// track before it ever calls WriteSample, and again after any mid-utterance
// starvation (see driveSegmenter's own doc comment for the full mechanism).
//
// Root cause this exists for: driveSegmenter used to hand every PCM chunk
// straight from the GPT-Live websocket to lkmedia.PCMLocalTrack the instant
// it arrived. That track paces playout on its own fixed ticker
// (server-sdk-go's pcmlocaltrack.go, processSamples/getFrameFromChunkBuffer)
// and emits a frame of pure silence whenever its buffer is empty at a tick —
// and the MQTT gateway downstream drops silent frames before they ever reach
// the device. So any burstiness in OpenAI's own delivery (confirmed present:
// see the DEBUG arrival-vs-realtime log below) became silence in the track,
// got dropped by the gateway, and starved the device even though not one
// packet was lost end to end. The Python livekit-agents framework, observed
// fine on the same gateway, buffers about a second ahead of playout; we
// buffered nothing.
//
// 500ms was deliberately less than that second, but it was not free: every
// reply reached the child roughly that much later than it would with zero
// lead. That added latency was the accepted trade for smooth, un-dropped
// playout — a stuttering greeting is worse than a marginally slower one — for
// as long as the burstiness it was covering for was believed to be real and
// upstream.
//
// It was not, or at least not entirely. GPT-Live audio jitter was root-caused
// (see pkg/gptlive/session.go's EventOutputAudioDelta comment) to the read
// goroutine there blocking on a slow downstream consumer, which throttled the
// websocket read itself via TCP backpressure — what this pipeline measured as
// OpenAI's own arrival rate was largely our own consumption rate. A
// standalone probe against OpenAI's real delivery (dev box, 4 configs at
// 16kHz/24kHz, with/without delegation, short/20KB instructions, ~44s of
// continuous speech each) measured an arrival ratio of 0.988-1.001 with ZERO
// gaps over 200ms in every config, against 0.66 measured on this worker at
// the same time under the same load. OpenAI delivers, for all practical
// purposes, in real time with nothing bursty to absorb.
//
// Now that pkg/gptlive's read goroutine can never be blocked by a slow
// consumer (it hands audio to an internal queue that never blocks, drained
// into Audio() by a separate goroutine — see audioQueue in session.go), the
// jitter this buffer was actually covering for is gone at the source. What is
// left to absorb is purely local: goroutine-scheduling gaps between that
// feeder, this loop, and the local track's own playout ticker — on the order
// of a scheduling quantum, not hundreds of milliseconds. So the lead is CUT,
// not removed outright, to exactly one driveSegmenter tick: enough slack to
// smooth a missed scheduling window (the same reasoning the 100ms ticker
// period below already rests on) without paying for jitter that the probe
// shows does not exist upstream anymore. This recovers 400 of the previous
// 500ms of reply latency.
const audioLeadBuffer = 100 * time.Millisecond

// audioLeadQueueCap bounds how much audio driveSegmenter will hold in its
// pre-lead queue, so a source that never lets audioLeadBuffer's duration
// check trip (an unexpectedly tiny or zero-length chunk stream, or any other
// bug in that arithmetic) cannot grow the queue's memory without limit. A few
// seconds is far more than audioLeadBuffer itself ever needs, so this is a
// backstop, not a tuning knob.
const audioLeadQueueCap = 3 * time.Second

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
	sess *gptlive.Session
	seg  *gptlive.Segmenter

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
	pendingGreet  bool
	agentText     map[string]string // burst id -> latest full text
	openAgentIDs  []string          // transcript ids seen since the burst opened
	transcript    []PersistedChatMessage
	voiceSeconds  float64
	backendTokens int
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
		eventsDone: make(chan struct{}),
	}
	p.seg = gptlive.NewSegmenter(p.onBurstOpen, p.onBurstClose)
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

	var defs []map[string]any
	var exec gptlive.ToolExecutor
	if p.spec.Tools != nil {
		defs = GPTLiveToolDefs(p.spec.Tools, p.spec.WebSearch)
		exec = NewRegistryExecutor(p.spec.Tools, p.rs.roomName())
	}
	sess, err := gptlive.Dial(ctx, gptlive.Config{
		APIKey: p.spec.APIKey, Voice: p.spec.Voice, SampleRate: p.spec.SampleRate,
		Instructions: p.spec.Persona.Voice,
		Backend:      gptlive.ResponsesConfig{Model: p.spec.BackendModel, Instructions: p.spec.Persona.Backend, Tools: defs},
		Tools:        exec, MaxSessionDuration: p.spec.MaxSessionDuration,
	})
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

// finishStart runs once Dial has returned a live session: it publishes
// p.sess/p.cancel/p.ready and either spawns the pumps or, if ctx is already
// cancelled, self-closes instead (see Start's doc comment for why). Split out
// from Start so this decision can be exercised directly in a test without a
// real dial (TestFinishStartSelfClosesWhenContextAlreadyCancelled).
func (p *gptLivePipeline) finishStart(ctx context.Context, sess *gptlive.Session) error {
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
	go p.pumpEvents(pctx)
	go p.pumpPublish(pctx)
	go func() {
		select {
		case <-time.After(greetingFallbackDelay):
			p.Greet()
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
		p.Greet()
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
	if p.sess != nil {
		p.sess.PushAudio(pcm16ToBytes(sample))
	}
	return nil
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
func (p *gptLivePipeline) Greet() {
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
	sess := p.sess
	p.mu.Unlock()
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

// driveSegmenter is the single loop that both drains audio and drives the
// Segmenter: segment.go documents, verbatim, that Feed and Tick "must be
// serialized... not safe for concurrent use". Task 12 review found them
// running on two separate goroutines (pumpAudioOut and a since-removed
// tickSegmenter), which could run Feed and Tick concurrently and let a burst
// get closed twice — silently dropping the second pass's transcript. Reading
// both the audio channel and the ticker in one select serializes every call
// into a single goroutine, structurally: Feed and Tick can never execute
// concurrently because nothing but this loop ever calls either one. Taking
// the channels as parameters (rather than reading p.sess.Audio() and a ticker
// it owns) is what lets TestDriveSegmenterSerializesFeedAndTick exercise this
// under concurrent producers without a real dialed session. The same is true
// of the track parameter added below: it lets the lead-buffer tests substitute
// a fake in place of a real *lkmedia.PCMLocalTrack.
//
// Lead buffer (see audioLeadBuffer's own doc comment for the starvation chain
// this fixes): every chunk arriving off audio is held in leadQueue, unwritten,
// until leadQueue holds audioLeadBuffer's worth of audio — "priming" below —
// at which point the whole queue is flushed to the track in one go and every
// further chunk is written the moment it arrives, keeping the track's own
// buffer topped up rather than fed just-in-time. bufferAhead tracks, in wall-
// clock terms, how far ahead of real time those writes have put the track: it
// grows by a chunk's own duration on every write and decays by real elapsed
// time on every tick. If it ever reaches zero while a burst is still open, the
// track's buffer is presumed to have run dry despite everything this loop
// already did — a real gap in GPT-Live's own delivery — so priming is
// re-armed for whatever arrives next rather than writing a single fresh chunk
// into an already-starving track: one chunk followed by another gap is
// exactly the short-lived padding the gateway's isSilent check drops, so a
// single dribbled write buys nothing and is worse than waiting to refill.
//
// p.seg.Feed is called here, at write time, deliberately not at arrival time:
// the ADR makes the Segmenter the one boundary for agent-state transitions,
// and those must track what the device will actually receive, not what
// merely arrived from the websocket up to audioLeadBuffer earlier.
func (p *gptLivePipeline) driveSegmenter(ctx context.Context, audio <-chan []byte, ticks <-chan time.Time, track localOutTrackWriter) {
	frameDur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(p.spec.SampleRate) }
	// leave() closes the local track before Close's cancel() has stopped this
	// loop, so every audio frame still queued at that moment fails to write —
	// a burst of identical warnings on EVERY teardown, one per frame. Log the
	// first and count the rest, then report the total once on the way out.
	writeErrs := 0
	defer func() {
		if writeErrs > 1 {
			logger.WarnCF("livekit", "gptlive: further writes to the local track failed", map[string]any{
				"room":       p.rs.roomName(),
				"suppressed": writeErrs - 1,
			})
		}
	}()

	// maxLeadQueueBytes backs audioLeadQueueCap: computed from byte length
	// rather than trusting frameDur's own duration arithmetic, so a bug there
	// (or a degenerate zero-length chunk stream) cannot defeat the very bound
	// meant to catch it.
	maxLeadQueueBytes := int(audioLeadQueueCap.Seconds() * float64(p.spec.SampleRate) * 2)

	var leadQueue [][]byte
	leadQueuedBytes := 0
	priming := true // session start is itself the first "after a starvation" case

	var bufferAhead time.Duration
	var lastTick time.Time

	// Arrival-vs-realtime accounting for the DEBUG log below (bytes actually
	// received off the websocket, against wall-clock time, independent of
	// however long this loop then held them before writing): reset whenever a
	// fresh run of arrivals begins, logged once per Segmenter burst so the
	// next dev-box test yields direct evidence of whether OpenAI delivers
	// faster or slower than real time.
	//
	// The window this measures is first-arrival to last-arrival, deliberately
	// NOT "first arrival to now": logArrivalRatio runs when the Segmenter
	// closes the burst, which is segmentIdle (800ms, segment.go) after the
	// last Feed, and Feed itself now happens at write time, up to
	// audioLeadBuffer (~500ms) after the chunk it feeds actually arrived.
	// Neither of those delays has anything to do with how fast audio arrived
	// off the websocket, so counting them into elapsed drags a true ratio near
	// 1.0 down to something that looks like OpenAI delivering below realtime
	// when it isn't. lastArrivalAt is stamped on every arrival (not on Feed),
	// so lastArrivalAt.Sub(arrivalStart) covers only the arrivals themselves.
	var arrivalStart time.Time
	var lastArrivalAt time.Time
	arrivalBytes := 0
	logArrivalRatio := func() {
		if arrivalStart.IsZero() || p.spec.SampleRate <= 0 {
			return
		}
		arrivalSpan := lastArrivalAt.Sub(arrivalStart)
		burstWall := time.Since(arrivalStart)
		bytesSeen := arrivalBytes
		arrivalStart = time.Time{}
		lastArrivalAt = time.Time{}
		arrivalBytes = 0
		if arrivalSpan <= 0 {
			// A single-chunk run (or clock oddity) has zero arrival span: no
			// meaningful rate to report, and dividing by it would be either a
			// crash or a nonsense ratio.
			return
		}
		expectedBytesPerSec := float64(p.spec.SampleRate) * 2 // PCM16 mono: 2 bytes/sample
		actualBytesPerSec := float64(bytesSeen) / arrivalSpan.Seconds()
		logger.DebugCF("livekit", "gptlive: audio arrival rate vs realtime", map[string]any{
			"room":            p.rs.roomName(),
			"ratio":           actualBytesPerSec / expectedBytesPerSec,
			"bytes_received":  bytesSeen,
			"elapsed_ms":      arrivalSpan.Milliseconds(),
			"arrival_span_ms": arrivalSpan.Milliseconds(),
			"burst_wall_ms":   burstWall.Milliseconds(),
			"sample_rate":     p.spec.SampleRate,
		})
	}

	// write pushes one chunk to the track (if any) and to the segmenter, in
	// that order specified by requirement 4 above (Feed at write time), and
	// reports a Segmenter burst that closes as a result so the caller can log
	// the arrival-rate ratio for it.
	write := func(pcm []byte) {
		wasOpen := p.seg.Open()
		p.seg.Feed(pcm, frameDur(len(pcm)))
		if wasOpen && !p.seg.Open() {
			logArrivalRatio()
		}
		bufferAhead += frameDur(len(pcm))
		if track != nil {
			if err := track.WriteSample(bytesToPCM16(pcm)); err != nil {
				writeErrs++
				if writeErrs == 1 {
					logger.WarnCF("livekit", "gptlive: write to local track", map[string]any{"error": err.Error()})
				}
			}
		}
	}
	flushQueue := func() {
		for _, pcm := range leadQueue {
			write(pcm)
		}
		leadQueue = nil
		leadQueuedBytes = 0
	}

	for {
		select {
		case pcm, ok := <-audio:
			if !ok {
				// Channel close: flush whatever is queued regardless of the
				// lead, so the tail of the last utterance is never abandoned
				// waiting for a lead that will now never arrive.
				flushQueue()
				return
			}
			if arrivalStart.IsZero() {
				arrivalStart = time.Now()
			}
			lastArrivalAt = time.Now()
			arrivalBytes += len(pcm)
			if priming {
				leadQueue = append(leadQueue, pcm)
				leadQueuedBytes += len(pcm)
				if frameDur(leadQueuedBytes) >= audioLeadBuffer || leadQueuedBytes >= maxLeadQueueBytes {
					priming = false
					flushQueue()
				}
			} else {
				write(pcm)
			}
		case now := <-ticks:
			if !lastTick.IsZero() {
				bufferAhead -= now.Sub(lastTick)
				if bufferAhead < 0 {
					bufferAhead = 0
				}
			}
			lastTick = now
			if bufferAhead == 0 && !priming {
				// The lead this loop built up has been fully spent against
				// wall-clock time: re-arm rather than let the next arriving
				// chunk dribble straight through into a track that is, per
				// this same accounting, already starving.
				priming = true
			}
			wasOpen := p.seg.Open()
			p.seg.Tick(now)
			if wasOpen && !p.seg.Open() {
				logArrivalRatio()
			}
		case <-ctx.Done():
			// Drain fully so teardown does not hang and no queued audio is
			// abandoned, matching the channel-close path above.
			flushQueue()
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

func (p *gptLivePipeline) onBurstOpen() { p.setState("speaking") }

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
