package livekit

import (
	"context"
	"encoding/binary"
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

// greetingFallbackDelay is how long Start waits for a "ready_for_greeting" data
// message before greeting on its own — mirroring the cascade's own fallback
// greeting behaviour so a lost/late control message never leaves the child
// sitting in silence.
const greetingFallbackDelay = 3 * time.Second

// gptLivePipeline is the GPT-Live analogue of AudioPipeline: it owns the
// gptlive.Session for one room, pumps model audio out to the room's local
// track, pumps model events into transcript/state bookkeeping, and feeds room
// mic audio into the model. One instance per RoomSession, created and started
// from Join and torn down from leave() — never a package-level var.
type gptLivePipeline struct {
	rs   *RoomSession
	spec GPTLiveSessionSpec
	sess *gptlive.Session
	seg  *gptlive.Segmenter

	// localTrack is captured once, in Start, from rs.localTrack (which Join
	// creates and publishes immediately before calling Start). Reading
	// rs.localTrack directly from pumpAudioOut later would race with leave()
	// clearing that field under rs.mu; this field is set exactly once, before
	// pumpAudioOut is spawned, and never mutated afterwards, so it needs no
	// lock of its own.
	localTrack *lkmedia.PCMLocalTrack

	mu            sync.Mutex
	state         string
	greeted       bool
	agentText     map[string]string // burst id -> latest full text
	openAgentIDs  []string          // transcript ids seen since the burst opened
	transcript    []PersistedChatMessage
	voiceSeconds  float64
	backendTokens int
	cancel        context.CancelFunc
}

// newGPTLivePipeline builds the pipeline; call Start to dial the model and
// begin its pumps. state starts as "listening" to match the "lk.agent.state"
// attribute the room token already carries at mint time (generateRoomToken),
// so the first real transition published is a genuine speaking/listening edge
// rather than a synthetic bootstrap one.
func newGPTLivePipeline(rs *RoomSession, spec GPTLiveSessionSpec) *gptLivePipeline {
	p := &gptLivePipeline{rs: rs, spec: spec, state: "listening", agentText: map[string]string{}}
	p.seg = gptlive.NewSegmenter(p.onBurstOpen, p.onBurstClose)
	if spec.Quiz != nil {
		// Score (agent_bridge-style contract documented on QuizTrackerConfig) calls
		// this synchronously, in-process, after releasing its own lock — never
		// concurrently with itself — so appending straight to the session here needs
		// no extra synchronization on this pipeline's side. By the time any quiz
		// tool can fire, p.sess is already set: it is only reachable through a
		// function call the model made over this same p.sess, which Start assigns
		// before the session can receive one.
		spec.Quiz.OnDirective(func(d string) {
			if p.sess != nil {
				p.sess.AppendInstructions(d)
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

// Start dials GPT-Live with the persona and tools and begins the pumps that
// move audio and events between the model and the room. ctx should be
// rs.ctx (not Join's own ctx parameter) so that leave()'s rs.cancel() stops
// every goroutine started here.
func (p *gptLivePipeline) Start(ctx context.Context) error {
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
		return err
	}
	p.sess = sess
	pctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.pumpAudioOut(pctx)
	go p.pumpEvents(pctx)
	go p.tickSegmenter(pctx)
	go func() {
		select {
		case <-time.After(greetingFallbackDelay):
			p.Greet()
		case <-pctx.Done():
		}
	}()
	return nil
}

// WriteSample is the PCMRemoteTrack writer for room mic audio: it goes
// straight to the model at the session's own sample rate. lkmedia has already
// resampled/downmixed the room track to that rate before this is called, so
// pkg/gptlive itself never resamples anything.
func (p *gptLivePipeline) WriteSample(sample media.PCM16Sample) error {
	if p.sess != nil {
		p.sess.PushAudio(pcm16ToBytes(sample))
	}
	return nil
}

// Greet asks the voice model to speak the character's greeting immediately.
// Idempotent: only the first caller (the "ready_for_greeting" data message or
// the fallback timer, whichever comes first) has any effect.
func (p *gptLivePipeline) Greet() {
	p.mu.Lock()
	if p.greeted || p.sess == nil {
		p.mu.Unlock()
		return
	}
	p.greeted = true
	p.mu.Unlock()
	p.sess.AppendCommentary("Immediately follow the instruction below. Do not wait for the caller to speak first. After that, pause and listen.\n\n" + p.spec.Persona.Greeting)
}

// SayGoodbye is the GPT-Live equivalent of the cascade's farewell: there is no
// separate TTS pipeline to hand a fixed sentence to, so the model is asked to
// speak its own goodbye. Unlike Greet, this is not idempotent — it is only
// ever called once, from handleGPTLiveEndPrompt, right before the room is left.
func (p *gptLivePipeline) SayGoodbye(prompt string) {
	if p.sess == nil {
		return
	}
	p.sess.AppendCommentary("Say a short, warm goodbye to the child now, along these lines, then stop talking: " + prompt)
}

func (p *gptLivePipeline) pumpAudioOut(ctx context.Context) {
	frameDur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(p.spec.SampleRate) }
	for {
		select {
		case pcm, ok := <-p.sess.Audio():
			if !ok {
				return
			}
			p.seg.Feed(pcm, frameDur(len(pcm)))
			if p.localTrack != nil {
				if err := p.localTrack.WriteSample(bytesToPCM16(pcm)); err != nil {
					logger.WarnCF("livekit", "gptlive: write to local track", map[string]any{"error": err.Error()})
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) tickSegmenter(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			p.seg.Tick(now)
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) pumpEvents(ctx context.Context) {
	for {
		select {
		case ev, ok := <-p.sess.Events():
			if !ok {
				return
			}
			p.onEvent(ev)
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) onEvent(ev gptlive.Event) {
	switch e := ev.(type) {
	case gptlive.UserTranscript:
		p.publishTranscript(e.ID, e.Text, e.Final, true)
		if e.Final {
			p.mu.Lock()
			p.transcript = append(p.transcript, PersistedChatMessage{ChatType: chatTypeUser, Content: e.Text, Timestamp: time.Now().UnixMilli()})
			p.mu.Unlock()
		}
	case gptlive.AgentTranscript:
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
		p.publishTranscript(e.ID, e.Text, false, false)
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
		// last update, so the latest value replaces rather than accumulates.
		p.mu.Lock()
		p.voiceSeconds = e.Seconds
		p.mu.Unlock()
	case gptlive.BackendUsage:
		// Total is per backend response (one per delegation), so summing across
		// every response completed this session gives the session total.
		p.mu.Lock()
		p.backendTokens += e.Total
		p.mu.Unlock()
	case gptlive.Closed:
		p.mu.Lock()
		p.voiceSeconds = e.VoiceSeconds
		p.mu.Unlock()
	case gptlive.Error:
		logger.WarnCF("livekit", "gptlive: session error", map[string]any{"error": e.Err.Error(), "recoverable": e.Recoverable})
	}
}

func (p *gptLivePipeline) onBurstOpen() { p.setState("speaking") }

// onBurstClose finalises the agent turn: publish the final transcript and record it.
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
}

// setState is the only place that changes p.state and publishes it, called
// exclusively from the segmenter's onBurstOpen/onBurstClose callbacks (plus
// the initial value set in newGPTLivePipeline) — the ADR's "segmenter is the
// one boundary" for agent state. It never holds p.mu while publishing: both
// the data-channel publish and the attribute update can block on network I/O.
func (p *gptLivePipeline) setState(next string) {
	p.mu.Lock()
	prev := p.state
	p.state = next
	p.mu.Unlock()
	if prev == next || p.rs == nil || p.rs.room == nil {
		return
	}
	p.rs.room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": next})
	_ = p.rs.PublishAgentState(prev, next)
}

// publishTranscript mirrors the cascade's lk.transcription text-stream shape
// (lk.segment_id, lk.final, and lk.transcribed_track_id for the child's own
// speech) so the dashboard/gateway need no changes to understand a GPT-Live
// session's transcripts.
func (p *gptLivePipeline) publishTranscript(segmentID, text string, final, user bool) {
	if p.rs == nil || p.rs.room == nil || text == "" {
		return
	}
	attrs := map[string]string{"lk.segment_id": segmentID, "lk.final": map[bool]string{true: "true", false: "false"}[final]}
	if user {
		attrs["lk.transcribed_track_id"] = p.rs.remoteAudioTrackSID()
	}
	_ = p.rs.room.LocalParticipant.SendText(text, lksdk.StreamTextOptions{Topic: "lk.transcription", Attributes: attrs})
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

// Close tears down the underlying gptlive.Session (which waits for the
// service's final usage report, bounded by ctx) and cancels every pump
// goroutine Start spawned. Called exactly once, from leave().
func (p *gptLivePipeline) Close(ctx context.Context) {
	if p.sess != nil {
		_ = p.sess.Close(ctx)
	}
	if p.cancel != nil {
		p.cancel()
	}
}
