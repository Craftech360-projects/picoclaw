package livekit

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/media-sdk"
	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

var voiceProviderChannelMarkerRE = regexp.MustCompile(`<\|channel\>[^<]*<channel\|>`)
var dynamicGreetingCooldownUntilUnix atomic.Int64

func dynamicGreetingRateLimited() bool {
	return time.Now().Unix() < dynamicGreetingCooldownUntilUnix.Load()
}

func markDynamicGreetingRateLimited(cooldown time.Duration) {
	if cooldown <= 0 {
		cooldown = 2 * time.Minute
	}
	until := time.Now().Add(cooldown).Unix()
	dynamicGreetingCooldownUntilUnix.Store(until)
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	lowerErr := strings.ToLower(err.Error())
	return strings.Contains(lowerErr, "429") || strings.Contains(lowerErr, "rate-limit") || strings.Contains(lowerErr, "rate limit")
}

func sanitizeVoiceTextForTTS(text string) string {
	text = voiceProviderChannelMarkerRE.ReplaceAllString(text, "")
	text = strings.NewReplacer(
		"<|channel|>", "",
		"<|message|>", "",
		"<|start|>", "",
		"<|end|>", "",
		"<|channel>", "",
		"<channel|>", "",
	).Replace(text)
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func normalizeUtteranceForDuplicateCheck(text string) string {
	return strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(text), " ")))
}

func mergeFinalTranscriptChunk(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}

	normalizedCurrent := normalizeUtteranceForDuplicateCheck(current)
	normalizedNext := normalizeUtteranceForDuplicateCheck(next)
	switch {
	case normalizedNext == normalizedCurrent:
		return current
	case strings.HasPrefix(normalizedNext, normalizedCurrent):
		return next
	case strings.HasPrefix(normalizedCurrent, normalizedNext):
		return current
	default:
		return current + " " + next
	}
}

type speechChunkDeduper struct {
	last string
}

func (d *speechChunkDeduper) ShouldSpeak(text string) bool {
	normalized := normalizeUtteranceForDuplicateCheck(sanitizeVoiceTextForTTS(text))
	if normalized == "" {
		return false
	}
	if len(normalized) >= 12 && normalized == d.last {
		return false
	}
	d.last = normalized
	return true
}

// sentenceSplitter accumulates text and emits complete sentences using a tokenizer.
type sentenceSplitter struct {
	buf       strings.Builder
	tokenizer *sentences.DefaultSentenceTokenizer
}

func newSentenceSplitter() *sentenceSplitter {
	tokenizer, err := english.NewSentenceTokenizer(nil)
	if err != nil {
		// Fallback to a simple splitter if the tokenizer fails to initialize
		return &sentenceSplitter{}
	}
	return &sentenceSplitter{
		tokenizer: tokenizer,
	}
}

func (s *sentenceSplitter) Feed(r rune) string {
	// Skip newlines entirely — they don't represent sentence boundaries for TTS
	// and would cause tiny fragments like "(Verse 1)" to be sent as separate TTS calls.
	if r == '\n' || r == '\r' {
		// Replace with a space to avoid words merging across lines
		if s.buf.Len() > 0 {
			s.buf.WriteRune(' ')
		}
		return ""
	}

	s.buf.WriteRune(r)
	text := s.buf.String()

	// Only attempt to split if we have a potential sentence or clause boundary
	if r == '.' || r == '!' || r == '?' || r == ',' || r == ';' || r == ':' {
		if s.tokenizer != nil {
			sentences := s.tokenizer.Tokenize(text)
			if len(sentences) > 1 {
				// We have at least one complete sentence and some remainder
				completeSentence := sentences[0].Text

				// Keep the rest in the buffer
				s.buf.Reset()
				for i := 1; i < len(sentences); i++ {
					s.buf.WriteString(sentences[i].Text)
					if i < len(sentences)-1 {
						s.buf.WriteString(" ")
					}
				}
				return completeSentence
			} else if r == ',' || r == ';' || r == ':' {
				// For commas and other clause boundaries, flush if the clause is long enough
				// Use a higher threshold (40 chars) to avoid tiny TTS fragments
				if len(text) > 40 {
					s.buf.Reset()
					return text
				}
			}
		} else {
			// Fallback to simple splitting
			return s.simpleSplit(r)
		}
	}
	return ""
}

func (s *sentenceSplitter) simpleSplit(r rune) string {
	if r == '.' || r == '!' || r == '?' || r == ',' || r == ';' || r == ':' {
		sentence := s.buf.String()
		if (r == ',' || r == ';' || r == ':') && len(sentence) <= 15 {
			return ""
		}
		s.buf.Reset()
		return sentence
	}
	return ""
}

func (s *sentenceSplitter) Flush() string {
	remaining := s.buf.String()
	s.buf.Reset()
	return remaining
}

// AudioPipeline coordinates STT -> Agent -> TTS for one participant in a room.
type AudioPipeline struct {
	session              *RoomSession
	bridge               *AgentBridge
	tts                  tts.Provider
	vadEvent             <-chan interface{}
	primaryLanguage      string // used for language-aware error fallback phrases
	turns                voiceTurnController
	publishSpeechCreated func()
}

type voiceTurnController struct {
	mu     sync.Mutex
	nextID uint64
	active voiceTurn
}

type voiceTurn struct {
	id     uint64
	ctx    context.Context
	cancel context.CancelFunc
	reason string
}

type turnLatencyMetaKey struct{}

type turnLatencyMeta struct {
	mu sync.Mutex

	TurnID    uint64
	Session   string
	Path      string
	Trigger   string
	STTStart  time.Time
	LLMStart  time.Time
	LLMFirst  time.Time
	LLMFinal  time.Time
	TTSFirst  time.Time
	TTSFinal  time.Time
	Completed bool
}

func (c *voiceTurnController) Start(parent context.Context, reason string) voiceTurn {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel != nil {
		c.active.reason = reason
		c.active.cancel()
	}
	c.nextID++
	ctx, cancel := context.WithCancel(parent)
	c.active = voiceTurn{id: c.nextID, ctx: ctx, cancel: cancel}
	return c.active
}

func (c *voiceTurnController) Cancel(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel == nil {
		return
	}
	c.active.reason = reason
	c.active.cancel()
}

func (c *voiceTurnController) IsActive(turn voiceTurn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return turn.id != 0 && c.active.id == turn.id && c.active.cancel != nil
}

func (c *voiceTurnController) Finish(turn voiceTurn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn.id != 0 && c.active.id == turn.id {
		c.active = voiceTurn{}
	}
}

func (c *voiceTurnController) CurrentID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active.id
}

func (ap *AudioPipeline) attachTurnLatencyMeta(turn voiceTurn, meta *turnLatencyMeta) voiceTurn {
	if turn.ctx == nil || meta == nil {
		return turn
	}
	turn.ctx = context.WithValue(turn.ctx, turnLatencyMetaKey{}, meta)
	return turn
}

func latencyMetaFromContext(ctx context.Context) *turnLatencyMeta {
	if ctx == nil {
		return nil
	}
	meta, _ := ctx.Value(turnLatencyMetaKey{}).(*turnLatencyMeta)
	return meta
}

func (ap *AudioPipeline) logTurnLatency(meta *turnLatencyMeta, marker string, duration time.Duration, extra map[string]any) {
	if meta == nil {
		return
	}
	fields := map[string]any{
		"turn_id":      meta.TurnID,
		"session":      meta.Session,
		"path":         meta.Path,
		"trigger":      meta.Trigger,
		"marker":       marker,
		"elapsed_ms":   duration.Milliseconds(),
		"timestamp_ms": time.Now().UnixMilli(),
	}
	for k, v := range extra {
		fields[k] = v
	}
	logger.InfoCF("livekit", "Turn latency marker", fields)
}

func (ap *AudioPipeline) finalizeTurnLatency(meta *turnLatencyMeta, reason string) {
	if meta == nil {
		return
	}
	meta.mu.Lock()
	defer meta.mu.Unlock()
	if meta.Completed {
		return
	}
	meta.Completed = true
	base := meta.LLMStart
	if !meta.STTStart.IsZero() {
		base = meta.STTStart
	}
	if !base.IsZero() {
		ap.logTurnLatency(meta, "turn_total_e2e", time.Since(base), map[string]any{"finalize_reason": reason})
	}
}

func (ap *AudioPipeline) emitRuntimeEvent(kind, sessionKey, cause, errText string, meta map[string]any) {
	if ap == nil || ap.bridge == nil {
		return
	}
	evt := RuntimeEvent{
		Kind:       kind,
		SessionKey: sessionKey,
		Cause:      cause,
		Error:      errText,
		Metadata:   meta,
	}
	_ = ap.bridge.EmitRuntimeEvent(evt)
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge, tts tts.Provider, vadEvent <-chan interface{}) *AudioPipeline {
	var lang string
	if session != nil {
		lang = session.primaryLanguage
	}
	ap := &AudioPipeline{
		session:         session,
		bridge:          bridge,
		tts:             tts,
		vadEvent:        vadEvent,
		primaryLanguage: lang,
	}
	ap.publishSpeechCreated = func() {
		if ap.session != nil {
			_ = ap.session.PublishSpeechCreated("")
		}
	}
	return ap
}

// HandleUtterance processes a complete user utterance: calls the agent and speaks the response.
func (ap *AudioPipeline) HandleUtterance(ctx context.Context, sessionKey string, text string, onDone func()) (bool, error) {
	if strings.TrimSpace(text) == "" {
		if onDone != nil {
			onDone()
		}
		return false, nil
	}
	if ap.bridge == nil {
		if onDone != nil {
			onDone()
		}
		return false, fmt.Errorf("agent bridge is nil")
	}

	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}
	firstChunkReceived := false
	latencyMeta := latencyMetaFromContext(ctx)
	var fillerCtx context.Context
	var fillerCancel context.CancelFunc

	if ap.session != nil {
		ap.session.PublishAgentState("listening", "thinking")
	}

	// Play filler word asynchronously so we can cancel it if LLM is fast
	if ap.session != nil && len(ap.session.fillerWords) > 0 {
		fillerCtx, fillerCancel = context.WithCancel(ctx)
		filler := ap.session.fillerWords[rand.Intn(len(ap.session.fillerWords))]
		go func() {
			ap.synthesizeAndPlay(fillerCtx, filler)
		}()
	}

	// ── Retry loop: up to 3 attempts with exponential back-off ───────────────
	const maxRetries = 3
	backoff := 200 * time.Millisecond

	// Build the chunk/done callbacks once; they close over splitter & firstChunkReceived.
	onChunk := func(chunk string) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !firstChunkReceived {
			firstChunkReceived = true
			if latencyMeta != nil {
				ap.logTurnLatency(latencyMeta, "llm_first_token", time.Since(latencyMeta.LLMStart), nil)
			}
			if ap.session != nil {
				ap.session.PublishAgentState("thinking", "speaking")
				ap.publishSpeechCreated()
			}
			if fillerCancel != nil {
				fillerCancel()                       // Cancel filler if LLM starts responding quickly
				ap.cancelTTS("llm_response_started") // clear any buffered filler audio
			}
		}
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				ap.synthesizeDeduped(ctx, speechDeduper, sentence)
			}
		}
	}
	onDoneCallback := func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// onDone is called when the LLM turn is finished (including async tools)
		if fillerCancel != nil {
			fillerCancel()
		}
		if remainder := splitter.Flush(); remainder != "" {
			ap.synthesizeDeduped(ctx, speechDeduper, remainder)
		}
		if latencyMeta != nil {
			ap.logTurnLatency(latencyMeta, "llm_final_token", time.Since(latencyMeta.LLMStart), nil)
		}
		ap.flushSilence(500) // add 500ms silence so device buffer doesn't clip the final word
		if ap.session != nil {
			ap.session.PublishAgentState("speaking", "listening")
		}
		if onDone != nil {
			onDone()
		}
	}

	var asyncPending bool
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Reset splitter on each retry so partial text doesn't bleed through
		if attempt > 0 {
			splitter = newSentenceSplitter()
			firstChunkReceived = false
		}
		asyncPending, err = ap.bridge.ChatStream(ctx, sessionKey, text, onChunk, onDoneCallback)
		if err == nil {
			break
		}
		if isContextCanceled(ctx, err) {
			interruptedErr := ctx.Err()
			if interruptedErr == nil {
				interruptedErr = err
			}
			ap.emitRuntimeEvent("interruption", sessionKey, "chatstream_context_canceled", interruptedErr.Error(), nil)
			logger.InfoCF("livekit", "ChatStream interrupted by canceled turn context", map[string]any{
				"session":       sessionKey,
				"cancel_reason": ap.currentTTSCancelReason(),
				"error":         err.Error(),
			})
			return false, interruptedErr
		}
		logger.WarnCF("livekit", "ChatStream failed, retrying", map[string]any{
			"attempt": attempt + 1,
			"max":     maxRetries,
			"error":   err.Error(),
			"session": sessionKey,
		})
		ap.emitRuntimeEvent("retry_scheduled", sessionKey, "chatstream_error", err.Error(), map[string]any{
			"attempt": attempt + 1,
			"max":     maxRetries,
		})
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				// Context cancelled (e.g. user interrupted) — don't retry
				if onDone != nil {
					onDone()
				}
				return false, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	if err != nil {
		// All retries exhausted — play a language-aware friendly fallback and clean up.
		fallback := ap.retryFallbackPhrase()
		ap.emitRuntimeEvent("fallback_used", sessionKey, "chatstream_retries_exhausted", err.Error(), nil)
		logger.ErrorCF("livekit", "All ChatStream retries failed — playing fallback", map[string]any{
			"session": sessionKey,
			"error":   err.Error(),
		})
		if fillerCancel != nil {
			fillerCancel()
		}
		ap.synthesizeAndPlay(ctx, fallback)
		if ap.session != nil {
			ap.session.PublishAgentState("speaking", "listening")
		}
		if onDone != nil {
			onDone()
		}
		return false, fmt.Errorf("agent (after %d retries): %w", maxRetries, err)
	}

	// If a tool is running asynchronously, we don't clear the context immediately.
	// The AgentBridge will call onDone when all iterations are complete.
	return asyncPending, nil
}

func (ap *AudioPipeline) HandleUtteranceForTurn(turn voiceTurn, sessionKey string, text string) (bool, error) {
	asyncPending, err := ap.HandleUtterance(turn.ctx, sessionKey, text, func() {
		if !ap.turns.IsActive(turn) {
			return
		}
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_done_callback")
		turn.cancel()
		ap.turns.Finish(turn)
	})
	if err != nil && isContextCanceled(turn.ctx, err) {
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_canceled")
	}
	if !asyncPending {
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_sync_complete")
		ap.turns.Finish(turn)
	}
	return asyncPending, err
}

// TriggerGreeting executes a proactive dynamic LLM greeting using the bridge.
// It bypasses the user speech wait loop and talks directly into the TTS pipeline.
// retryFallbackPhrase returns a child-friendly "I glitched, say again" phrase
// in the session's primary language so the child is not left in silence.
func (ap *AudioPipeline) retryFallbackPhrase() string {
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return "Oho! Mujhe thoda confusion ho gaya. Kya tum dobara bol sakte ho?"
	case "kannada":
		return "Oho! Nanage swalpa gond aaytu. Matte heli, please?"
	case "malayalam":
		return "Oho! Ente brain oru second confused ayi. Vere helo please?"
	case "tamil":
		return "Oho! Enakku konjam confusion achu. Mela solla mudiyuma?"
	case "telugu":
		return "Oho! Naaku koddiga confusion aindi. Malli cheppagalava?"
	default:
		return "Oops! My brain got a little confused just now. Can you say that again?"
	}
}

func (ap *AudioPipeline) greetingFallbackPhrase() string {
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return "Namaste! Main Cheeko hoon, tumhara dost. Chalo maze karte hain!"
	case "kannada":
		return "Namaskara! Nanu Cheeko, ninna snehita. Banni, fun madona!"
	case "malayalam":
		return "Namaskaram! Njan Cheeko anu, ninte suhruth. Namukku fun cheyyam!"
	case "tamil":
		return "Vanakkam! Naan Cheeko, un friend. Va, fun pannalaam!"
	case "telugu":
		return "Namaskaram! Nenu Cheeko, nee friend ni. Raa, fun cheddam!"
	default:
		return "Hi! I am Cheeko, your fun friend. Ready for an awesome chat?"
	}
}

func (ap *AudioPipeline) proactiveAnnouncementFallbackPhrase() string {
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return "Mujhe ek chhota update dena tha, par meri voice mein thoda hiccup aa gaya. Main jaldi phir try karti hoon."
	case "kannada":
		return "Nanage ondu chikka update helbekittu, aadre nanna voice-ge swalpa hiccup aaytu. Naanu swalpa time nantara matte try madtini."
	case "malayalam":
		return "Enikku oru cheriya update parayan undayirunnu, pakshe ente voice-il oru cheriya hiccup undayi. Njan kurachu kazhinju veendum try cheyyam."
	case "tamil":
		return "Unakku oru chinna update sollanum nu irundhen, aana en voice-ku konjam hiccup vandhuduchu. Naan konjam nerathula thirumbi try panren."
	case "telugu":
		return "Nenu oka chinna update cheppali anukunna, kani naa voice ki konchem hiccup vachindi. Konchem sepu taruvata malli try chestanu."
	default:
		return "I had a quick update for you, but my voice had a tiny hiccup. I will try again shortly."
	}
}

// TriggerGreeting executes a proactive dynamic LLM greeting using the bridge.
// It bypasses the user speech wait loop and talks directly into the TTS pipeline.

func (ap *AudioPipeline) TriggerGreeting(ctx context.Context, sessionKey string) {
	if ap.bridge == nil || ap.session == nil {
		return
	}
	if dynamicGreetingRateLimited() {
		turn := ap.turns.Start(ctx, "greeting_rate_limited_fallback")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "greeting",
			Trigger:  "cooldown_fallback",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		ap.session.PublishAgentState("listening", "speaking")
		ap.publishSpeechCreated()
		ap.synthesizeAndPlay(turn.ctx, ap.greetingFallbackPhrase())
		if ap.turns.IsActive(turn) {
			ap.flushSilenceForContext(turn.ctx, 300)
			ap.session.PublishAgentState("speaking", "listening")
		}
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "cooldown_fallback_complete")
		ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_cooldown_active", "", nil)
		logger.InfoCF("livekit", "Skipped dynamic greeting due to active rate-limit cooldown", map[string]any{
			"session": sessionKey,
		})
		return
	}

	logger.InfoCF("livekit", "Triggering dynamic agent greeting", map[string]any{
		"session": sessionKey,
	})

	ap.session.PublishAgentState("listening", "thinking")
	turn := ap.turns.Start(ctx, "greeting")
	turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
		TurnID:   turn.id,
		Session:  sessionKey,
		Path:     "greeting",
		Trigger:  "dynamic",
		LLMStart: time.Now(),
	})
	ap.setTTSCancel(turn.cancel)
	firstChunkReceived := false
	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}

	go func() {
		err := ap.bridge.GenerateGreeting(turn.ctx, sessionKey, func(chunk string) {
			if !ap.turns.IsActive(turn) {
				return
			}
			if !firstChunkReceived {
				firstChunkReceived = true
				if meta := latencyMetaFromContext(turn.ctx); meta != nil {
					ap.logTurnLatency(meta, "llm_first_token", time.Since(meta.LLMStart), nil)
				}
				ap.session.PublishAgentState("thinking", "speaking")
				ap.publishSpeechCreated()
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeDeduped(turn.ctx, speechDeduper, sentence)
				}
			}
		}, func() {
			if !ap.turns.IsActive(turn) {
				return
			}
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeDeduped(turn.ctx, speechDeduper, remainder)
			}
			if meta := latencyMetaFromContext(turn.ctx); meta != nil {
				ap.logTurnLatency(meta, "llm_final_token", time.Since(meta.LLMStart), nil)
			}
			ap.flushSilenceForContext(turn.ctx, 500)
			ap.session.PublishAgentState("speaking", "listening")
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "greeting_complete")
		})

		if err != nil {
			shouldFallback := !firstChunkReceived && turn.ctx.Err() == nil && ap.turns.IsActive(turn)
			// When the greeting LLM call is rate-limited, speak a local fallback
			// greeting so the child is not greeted with silence.
			if shouldFallback {
				if ap.session != nil {
					ap.session.PublishAgentState("listening", "speaking")
				}
				ap.publishSpeechCreated()
				ap.synthesizeAndPlay(turn.ctx, ap.greetingFallbackPhrase())
				ap.flushSilenceForContext(turn.ctx, 300)
				if ap.session != nil {
					ap.session.PublishAgentState("speaking", "listening")
				}
			}
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "greeting_error")
			if isRateLimitError(err) {
				markDynamicGreetingRateLimited(2 * time.Minute)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_rate_limited", err.Error(), nil)
				logger.WarnCF("livekit", "Dynamic greeting rate-limited; used fallback greeting", map[string]any{
					"session": sessionKey,
					"error":   err.Error(),
				})
			} else {
				// Non-429 provider failures can still storm proactive turns; apply a
				// shorter cooldown while preserving local fallback speech behavior.
				markDynamicGreetingRateLimited(30 * time.Second)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_provider_unavailable", err.Error(), nil)
				logger.ErrorCF("livekit", "Failed to generate dynamic greeting", map[string]any{
					"session": sessionKey,
					"error":   err.Error(),
				})
			}
		}
	}()
}

// RunInbound reads STT transcription events and calls the agent on speech end.
// It also listens for background task completions via the bridge's async event channel.
func (ap *AudioPipeline) RunInbound(ctx context.Context, sttStream stt.TranscriptionStream) {
	if sttStream == nil {
		return
	}
	var utterance strings.Builder
	var vadSpeechEnded bool
	var speechActive bool
	var latestTranscript string
	var finalizeTimer *time.Timer
	var finalizeTimerC <-chan time.Time
	var segmentTimer *time.Timer
	var segmentTimerC <-chan time.Time
	var lastFlushedText string
	var lastFlushedAt time.Time
	var sttSpeechStartAt time.Time
	var sttFirstPartialAt time.Time
	var sttFirstFinalAt time.Time
	var pendingBargeIn bool
	var hardCapFinalizePending bool

	const sttSegmentHardCap = 25 * time.Second

	// Get the async event channel from the bridge (may be nil)
	var asyncEvents <-chan AsyncEvent
	if ap.bridge != nil {
		asyncEvents = ap.bridge.AsyncEvents()
	}

	stopFinalizeTimer := func() {
		if finalizeTimer == nil {
			finalizeTimerC = nil
			return
		}
		if !finalizeTimer.Stop() {
			select {
			case <-finalizeTimer.C:
			default:
			}
		}
		finalizeTimer = nil
		finalizeTimerC = nil
	}

	stopSegmentTimer := func() {
		if segmentTimer == nil {
			segmentTimerC = nil
			return
		}
		if !segmentTimer.Stop() {
			select {
			case <-segmentTimer.C:
			default:
			}
		}
		segmentTimer = nil
		segmentTimerC = nil
	}

	startSegmentTimer := func() {
		if sttSegmentHardCap <= 0 {
			return
		}
		if segmentTimer == nil {
			segmentTimer = time.NewTimer(sttSegmentHardCap)
		} else {
			if !segmentTimer.Stop() {
				select {
				case <-segmentTimer.C:
				default:
				}
			}
			segmentTimer.Reset(sttSegmentHardCap)
		}
		segmentTimerC = segmentTimer.C
	}

	startFinalizeTimer := func() {
		const finalizeGracePeriod = 750 * time.Millisecond
		if finalizeTimer == nil {
			finalizeTimer = time.NewTimer(finalizeGracePeriod)
		} else {
			if !finalizeTimer.Stop() {
				select {
				case <-finalizeTimer.C:
				default:
				}
			}
			finalizeTimer.Reset(finalizeGracePeriod)
		}
		finalizeTimerC = finalizeTimer.C
	}

	flushBufferedUtterance := func(trigger string) {
		text := strings.TrimSpace(utterance.String())
		if text == "" {
			text = strings.TrimSpace(latestTranscript)
		}
		utterance.Reset()
		latestTranscript = ""
		vadSpeechEnded = false
		speechActive = false
		hardCapFinalizePending = false
		pendingBargeIn = false
		stopFinalizeTimer()
		stopSegmentTimer()

		if ap.session != nil && ap.session.participant != nil {
			ap.session.participant.speaking.Store(false)
		}

		if text == "" {
			return
		}
		speechStartSnapshot := sttSpeechStartAt
		firstPartialSnapshot := sttFirstPartialAt
		firstFinalSnapshot := sttFirstFinalAt
		sttSpeechStartAt = time.Time{}
		sttFirstPartialAt = time.Time{}
		sttFirstFinalAt = time.Time{}
		normalizedText := normalizeUtteranceForDuplicateCheck(text)
		if normalizedText != "" &&
			normalizedText == lastFlushedText &&
			time.Since(lastFlushedAt) < 2*time.Second {
			logger.DebugCF("livekit", "Suppressing duplicate speech end with same text", map[string]any{
				"session": ap.sessionKey(),
				"text":    text,
				"trigger": trigger,
			})
			return
		}
		lastFlushedText = normalizedText
		lastFlushedAt = time.Now()

		logger.DebugCF("livekit", "Speech end with text", map[string]any{
			"session": ap.sessionKey(),
			"text":    text,
			"trigger": trigger,
		})

		sessionKey := ap.sessionKey()
		if sessionKey == "" {
			return
		}

		turn := ap.turns.Start(ctx, "new_user_utterance")
		latencyMeta := &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "user_turn",
			Trigger:  trigger,
			STTStart: speechStartSnapshot,
			LLMStart: time.Now(),
		}
		turn = ap.attachTurnLatencyMeta(turn, latencyMeta)
		if !speechStartSnapshot.IsZero() && !firstPartialSnapshot.IsZero() {
			ap.logTurnLatency(latencyMeta, "stt_first_partial", firstPartialSnapshot.Sub(speechStartSnapshot), nil)
		}
		if !speechStartSnapshot.IsZero() && !firstFinalSnapshot.IsZero() {
			ap.logTurnLatency(latencyMeta, "stt_first_final", firstFinalSnapshot.Sub(speechStartSnapshot), nil)
		}
		ap.setTTSCancel(turn.cancel)

		go func() {
			_, _ = ap.HandleUtteranceForTurn(turn, sessionKey, text)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			stopFinalizeTimer()
			stopSegmentTimer()
			return

		case vadEvt, ok := <-ap.vadEvent:
			if !ok {
				ap.vadEvent = nil
				continue
			}
			if evt, ok := vadEvt.(vad.VADEvent); ok {
				if evt.SpeechStart {
					sttSpeechStartAt = time.Now()
					sttFirstPartialAt = time.Time{}
					sttFirstFinalAt = time.Time{}
					if vadSpeechEnded && (utterance.Len() > 0 || strings.TrimSpace(latestTranscript) != "") {
						flushBufferedUtterance("next_vad_start")
					}
					if ap.session != nil && ap.session.participant != nil {
						ap.session.participant.speaking.Store(true)
					}
					logger.DebugCF("livekit", "VAD Speech start", map[string]any{
						"session":     ap.sessionKey(),
						"probability": evt.Probability,
					})
					logger.DebugCF("livekit", "VAD speech start detected; waiting for transcript before interrupting agent audio", map[string]any{
						"session":     ap.sessionKey(),
						"probability": evt.Probability,
					})
					pendingBargeIn = true
					stopFinalizeTimer()
					vadSpeechEnded = false
					speechActive = true
					hardCapFinalizePending = false
					startSegmentTimer()
					lastFlushedText = ""
				}

				if evt.SpeechEnd {
					vadSpeechEnded = true
					speechActive = false
					hardCapFinalizePending = false
					stopSegmentTimer()
					logger.DebugCF("livekit", "VAD Speech end, finalizing STT stream", map[string]any{
						"session":     ap.sessionKey(),
						"probability": evt.Probability,
					})
					if err := sttStream.Finalize(); err != nil {
						logger.ErrorCF("livekit", "Failed to finalize STT stream", map[string]any{
							"session": ap.sessionKey(),
							"error":   err.Error(),
						})
					}
					startFinalizeTimer()
				}
			}

		case evt, ok := <-sttStream.Results():
			if !ok {
				logger.WarnCF("livekit", "STT stream closed, exiting RunInbound", map[string]any{
					"session": ap.sessionKey(),
				})
				return
			}

			if evt.Text != "" {
				latestTranscript = evt.Text
				if sttFirstPartialAt.IsZero() {
					sttFirstPartialAt = time.Now()
				}
				if pendingBargeIn {
					logger.InfoCF("livekit", "Transcript confirmed user speech; interrupting active agent audio if present", map[string]any{
						"session": ap.sessionKey(),
						"text":    evt.Text,
					})
					ap.cancelTTS("stt_transcript_after_vad")
					ap.turns.Cancel("stt_transcript_after_vad")
					pendingBargeIn = false
				}
			}

			if evt.IsFinal && evt.Text != "" {
				if sttFirstFinalAt.IsZero() {
					sttFirstFinalAt = time.Now()
				}
				merged := mergeFinalTranscriptChunk(utterance.String(), evt.Text)
				utterance.Reset()
				utterance.WriteString(merged)
			}

			if evt.SpeechEnd && hardCapFinalizePending && speechActive && !vadSpeechEnded {
				// Hard-cap finalize yields provider-level speech end, but user is still
				// speaking. Keep collecting transcript and rotate to the next segment.
				hardCapFinalizePending = false
				startSegmentTimer()
				continue
			}

			if evt.SpeechEnd || (vadSpeechEnded && utterance.Len() > 0) {
				trigger := "stt_speech_end"
				if vadSpeechEnded && !evt.SpeechEnd {
					trigger = "vad_with_final_text"
				}
				flushBufferedUtterance(trigger)
			}

		case <-finalizeTimerC:
			finalizeTimer = nil
			finalizeTimerC = nil
			if vadSpeechEnded && (utterance.Len() > 0 || strings.TrimSpace(latestTranscript) != "") {
				flushBufferedUtterance("vad_finalize_timeout")
			} else {
				pendingBargeIn = false
				vadSpeechEnded = false
				if ap.session != nil && ap.session.participant != nil {
					ap.session.participant.speaking.Store(false)
				}
			}

		case <-segmentTimerC:
			segmentTimer = nil
			segmentTimerC = nil
			if !speechActive || vadSpeechEnded {
				continue
			}
			logger.WarnCF("livekit", "STT hard-cap reached, finalizing rolling segment", map[string]any{
				"session":  ap.sessionKey(),
				"cap_secs": int(sttSegmentHardCap.Seconds()),
			})
			hardCapFinalizePending = true
			if err := sttStream.Finalize(); err != nil {
				hardCapFinalizePending = false
				logger.ErrorCF("livekit", "Failed to finalize STT stream on hard-cap", map[string]any{
					"session": ap.sessionKey(),
					"error":   err.Error(),
				})
				startSegmentTimer()
			}

		case asyncEvt, ok := <-asyncEvents:
			if !ok {
				asyncEvents = nil
				continue
			}
			ap.handleAsyncEvent(asyncEvt, vadSpeechEnded)
		}
	}
}

// handleAsyncEvent processes a background task completion event.
// If the user is currently speaking, the result is silently appended to history.
// If the user is NOT speaking, a spontaneous LLM + TTS response is triggered.
func (ap *AudioPipeline) handleAsyncEvent(evt AsyncEvent, userSpeaking bool) {
	sessionKey := ap.sessionKey()
	if sessionKey == "" {
		return
	}

	// Check if user is actively speaking
	isSpeaking := userSpeaking
	if ap.session != nil && ap.session.participant != nil {
		isSpeaking = ap.session.participant.speaking.Load()
	}

	if isSpeaking {
		// User is speaking — silently add to conversation history.
		// The LLM will naturally see it on the next user-initiated turn.
		logger.InfoCF("livekit", "Background task result queued silently (user speaking)", map[string]any{
			"tool":    evt.ToolName,
			"session": sessionKey,
		})
		if ap.bridge != nil && ap.bridge.sessions != nil && evt.Result != nil {
			ap.bridge.sessions.AddMessage(sessionKey, "user",
				"[Background Task Completed] "+evt.ToolName+": "+evt.Result.ContentForLLM())
		}
		return
	}

	// User is NOT speaking — trigger spontaneous announcement
	// Cron jobs already executed through the agent path. Announce their result
	// directly to avoid an extra LLM turn.
	if evt.ToolName == "cron" && evt.Result != nil {
		content := strings.TrimSpace(evt.Result.ContentForLLM())
		if content == "" {
			return
		}
		turn := ap.turns.Start(context.Background(), "background_cron_announcement")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "background_cron",
			Trigger:  "cron_direct_announcement",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		if ap.session != nil {
			ap.session.PublishAgentState("listening", "speaking")
			ap.publishSpeechCreated()
		}
		ap.synthesizeAndPlay(turn.ctx, content)
		if ap.turns.IsActive(turn) && ap.session != nil {
			ap.flushSilenceForContext(turn.ctx, 500)
			ap.session.PublishAgentState("speaking", "listening")
		}
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_cron_complete")
		return
	}

	logger.InfoCF("livekit", "Triggering spontaneous announcement for background task", map[string]any{
		"tool":    evt.ToolName,
		"session": sessionKey,
	})

	if dynamicGreetingRateLimited() {
		turn := ap.turns.Start(context.Background(), "background_task_cooldown_fallback")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "background_task",
			Trigger:  "cooldown_fallback",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		if ap.session != nil {
			ap.session.PublishAgentState("listening", "speaking")
			ap.publishSpeechCreated()
		}
		ap.synthesizeAndPlay(turn.ctx, ap.proactiveAnnouncementFallbackPhrase())
		if ap.turns.IsActive(turn) && ap.session != nil {
			ap.flushSilenceForContext(turn.ctx, 300)
			ap.session.PublishAgentState("speaking", "listening")
		}
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_cooldown_fallback_complete")
		ap.emitRuntimeEvent("fallback_used", sessionKey, "background_cooldown_active", "", map[string]any{
			"tool": evt.ToolName,
		})
		logger.InfoCF("livekit", "Skipped dynamic spontaneous response due to active rate-limit cooldown", map[string]any{
			"tool":    evt.ToolName,
			"session": sessionKey,
		})
		return
	}

	turn := ap.turns.Start(context.Background(), "background_task_result")
	turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
		TurnID:   turn.id,
		Session:  sessionKey,
		Path:     "background_task",
		Trigger:  evt.ToolName,
		LLMStart: time.Now(),
	})
	ap.setTTSCancel(turn.cancel)

	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}

	go func() {
		if ap.session != nil {
			ap.session.PublishAgentState("listening", "thinking")
		}
		firstChunkReceived := false

		err := ap.bridge.GenerateSpontaneousResponse(turn.ctx, sessionKey, evt, func(chunk string) {
			if !ap.turns.IsActive(turn) {
				return
			}
			if !firstChunkReceived {
				firstChunkReceived = true
				if meta := latencyMetaFromContext(turn.ctx); meta != nil {
					ap.logTurnLatency(meta, "llm_first_token", time.Since(meta.LLMStart), nil)
				}
				if ap.session != nil {
					ap.session.PublishAgentState("thinking", "speaking")
					ap.publishSpeechCreated()
				}
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeDeduped(turn.ctx, speechDeduper, sentence)
				}
			}
		}, func() {
			if !ap.turns.IsActive(turn) {
				return
			}
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeDeduped(turn.ctx, speechDeduper, remainder)
			}
			if meta := latencyMetaFromContext(turn.ctx); meta != nil {
				ap.logTurnLatency(meta, "llm_final_token", time.Since(meta.LLMStart), nil)
			}
			ap.flushSilenceForContext(turn.ctx, 500) // trailing silence for spontaneous replies
			if ap.session != nil {
				ap.session.PublishAgentState("speaking", "listening")
			}
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_task_complete")
		})
		if err != nil {
			shouldFallback := !firstChunkReceived && turn.ctx.Err() == nil && ap.turns.IsActive(turn)
			if shouldFallback {
				if ap.session != nil {
					ap.session.PublishAgentState("listening", "speaking")
					ap.publishSpeechCreated()
				}
				ap.synthesizeAndPlay(turn.ctx, ap.proactiveAnnouncementFallbackPhrase())
				ap.flushSilenceForContext(turn.ctx, 300)
				if ap.session != nil {
					ap.session.PublishAgentState("speaking", "listening")
				}
			}
			if isRateLimitError(err) {
				markDynamicGreetingRateLimited(2 * time.Minute)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "background_rate_limited", err.Error(), map[string]any{
					"tool": evt.ToolName,
				})
				logger.WarnCF("livekit", "Spontaneous response rate-limited; used fallback announcement", map[string]any{
					"error":   err.Error(),
					"tool":    evt.ToolName,
					"session": sessionKey,
				})
			} else {
				markDynamicGreetingRateLimited(30 * time.Second)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "background_provider_unavailable", err.Error(), map[string]any{
					"tool": evt.ToolName,
				})
				logger.ErrorCF("livekit", "Spontaneous response generation failed; used fallback announcement", map[string]any{
					"error":   err.Error(),
					"tool":    evt.ToolName,
					"session": sessionKey,
				})
			}
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_task_error")
			logger.DebugCF("livekit", "Spontaneous response turn finalized after error", map[string]any{
				"error":   err.Error(),
				"tool":    evt.ToolName,
				"session": sessionKey,
			})
		}
	}()
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	text = sanitizeVoiceTextForTTS(text)
	if text == "" {
		return
	}
	logger.DebugCF("livekit", "Synthesizing audio chunk", map[string]any{
		"session": ap.sessionKey(),
		"text":    text,
	})
	stream, err := ap.tts.Synthesize(ctx, text)
	if err != nil {
		if isContextCanceled(ctx, err) {
			logger.InfoCF("livekit", "TTS synthesize interrupted by canceled turn context", map[string]any{
				"session":       ap.sessionKey(),
				"cancel_reason": ap.currentTTSCancelReason(),
				"error":         err.Error(),
			})
			return
		}
		logger.ErrorCF("livekit", "TTS synthesize failed", map[string]any{
			"session": ap.sessionKey(),
			"error":   err.Error(),
		})
		return
	}
	defer stream.Close()

	logger.DebugCF("livekit", "Audio stream started", map[string]any{
		"session":   ap.sessionKey(),
		"track_sid": ap.localTrackSID(),
	})
	latencyMeta := latencyMetaFromContext(ctx)

	started := time.Now()
	wroteAudio := false
	chunksWritten := 0
	samplesWritten := 0
	for {
		select {
		case <-ctx.Done():
			logger.InfoCF("livekit", "TTS audio stream interrupted by canceled turn context", map[string]any{
				"session":         ap.sessionKey(),
				"track_sid":       ap.localTrackSID(),
				"cancel_reason":   ap.currentTTSCancelReason(),
				"chunks_written":  chunksWritten,
				"samples_written": samplesWritten,
				"duration_ms":     time.Since(started).Milliseconds(),
			})
			return
		default:
		}

		chunk, err := stream.Read()
		if err == io.EOF {
			if latencyMeta != nil {
				latencyMeta.mu.Lock()
				if latencyMeta.TTSFinal.IsZero() {
					latencyMeta.TTSFinal = time.Now()
					llmBase := latencyMeta.LLMStart
					sttBase := latencyMeta.STTStart
					latencyMeta.mu.Unlock()
					if !llmBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_final_audio", time.Since(llmBase), nil)
					}
					if !sttBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_final_audio_from_stt_start", time.Since(sttBase), nil)
					}
				} else {
					latencyMeta.mu.Unlock()
				}
			}
			logger.DebugCF("livekit", "Audio stream complete", map[string]any{
				"session":         ap.sessionKey(),
				"track_sid":       ap.localTrackSID(),
				"chunks_written":  chunksWritten,
				"samples_written": samplesWritten,
				"duration_ms":     time.Since(started).Milliseconds(),
			})
			return
		}
		if err != nil {
			if isContextCanceled(ctx, err) {
				logger.InfoCF("livekit", "TTS stream read interrupted by canceled turn context", map[string]any{
					"session":         ap.sessionKey(),
					"track_sid":       ap.localTrackSID(),
					"cancel_reason":   ap.currentTTSCancelReason(),
					"chunks_written":  chunksWritten,
					"samples_written": samplesWritten,
					"duration_ms":     time.Since(started).Milliseconds(),
					"error":           err.Error(),
				})
				return
			}
			logger.ErrorCF("livekit", "TTS stream read failed", map[string]any{
				"session":   ap.sessionKey(),
				"track_sid": ap.localTrackSID(),
				"error":     err.Error(),
			})
			return
		}
		if len(chunk) == 0 {
			continue
		}

		samples := bytesToPCM16(chunk)
		if len(samples) == 0 {
			continue
		}
		if err := ap.session.localTrack.WriteSample(samples); err != nil {
			logger.ErrorCF("livekit", "TTS write failed", map[string]any{
				"session": ap.sessionKey(),
				"error":   err.Error(),
			})
			return
		}
		chunksWritten++
		samplesWritten += len(samples)
		if !wroteAudio {
			wroteAudio = true
			if latencyMeta != nil {
				latencyMeta.mu.Lock()
				if latencyMeta.TTSFirst.IsZero() {
					latencyMeta.TTSFirst = time.Now()
					llmBase := latencyMeta.LLMStart
					sttBase := latencyMeta.STTStart
					latencyMeta.mu.Unlock()
					if !llmBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_first_audio", time.Since(llmBase), nil)
					}
					if !sttBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_first_audio_from_stt_start", time.Since(sttBase), nil)
					}
				} else {
					latencyMeta.mu.Unlock()
				}
			}
			minSample, maxSample, avgAbs := sampleStats(samples)
			logger.DebugCF("livekit", "TTS audio written", map[string]any{
				"session":      ap.sessionKey(),
				"sample_count": len(samples),
				"min_sample":   minSample,
				"max_sample":   maxSample,
				"avg_abs":      avgAbs,
			})
		}
	}
}

func (ap *AudioPipeline) synthesizeDeduped(ctx context.Context, deduper *speechChunkDeduper, text string) {
	if deduper != nil && !deduper.ShouldSpeak(text) {
		logger.InfoCF("livekit", "Suppressing duplicate assistant speech chunk", map[string]any{
			"session": ap.sessionKey(),
			"text":    sanitizeVoiceTextForTTS(text),
		})
		return
	}
	ap.synthesizeAndPlay(ctx, text)
}

func (ap *AudioPipeline) cancelTTS(reason string) {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	hadActiveTTS := ps.ttsCancel != nil
	if ps.ttsCancel != nil {
		ps.ttsCancelReason = reason
		ps.ttsCancel()
		ps.ttsCancel = nil
	}
	if ap.session.localTrack != nil {
		ap.session.localTrack.ClearQueue()
	}

	if hadActiveTTS {
		ap.session.PublishAgentState("speaking", "listening")
		ap.emitRuntimeEvent("interruption", ap.sessionKey(), reason, "", map[string]any{
			"had_active_tts": hadActiveTTS,
		})
	}

	logger.DebugCF("livekit", "TTS cancel/clear queue", map[string]any{
		"session":        ap.sessionKey(),
		"reason":         reason,
		"had_active_tts": hadActiveTTS,
		"track_sid":      ap.localTrackSID(),
	})
}

func (ap *AudioPipeline) setTTSCancel(cancel context.CancelFunc) {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	ps.ttsCancel = cancel
	ps.ttsCancelReason = ""
	ps.mu.Unlock()
}

func (ap *AudioPipeline) currentTTSCancelReason() string {
	if ap.session == nil || ap.session.participant == nil {
		return ""
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.ttsCancelReason
}

func isContextCanceled(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled)
}

func (ap *AudioPipeline) sessionKey() string {
	if ap.session == nil || ap.session.participant == nil {
		return ""
	}
	if ap.session.participant.sessionKey != "" {
		return ap.session.participant.sessionKey
	}
	if ap.session.roomInfo == nil {
		return ""
	}
	return fmt.Sprintf("livekit:%s:%s", ap.session.roomInfo.Name, ap.session.participant.identity)
}

func (ap *AudioPipeline) localTrackSID() string {
	if ap.session == nil {
		return ""
	}
	return ap.session.localTrackSID
}

func bytesToPCM16(b []byte) media.PCM16Sample {
	if len(b) < 2 {
		return nil
	}
	count := len(b) / 2
	out := make(media.PCM16Sample, count)
	for i := 0; i < count; i++ {
		off := i * 2
		out[i] = int16(binary.LittleEndian.Uint16(b[off : off+2]))
	}
	return out
}

func sampleStats(samples media.PCM16Sample) (int16, int16, int) {
	if len(samples) == 0 {
		return 0, 0, 0
	}

	minSample := samples[0]
	maxSample := samples[0]
	var totalAbs int64
	for _, sample := range samples {
		if sample < minSample {
			minSample = sample
		}
		if sample > maxSample {
			maxSample = sample
		}
		totalAbs += int64(math.Abs(float64(sample)))
	}

	return minSample, maxSample, int(totalAbs / int64(len(samples)))
}

// flushSilence pushes empty audio samples to ensure the end of speech is not cut off by network or device buffers.
func (ap *AudioPipeline) flushSilence(durationMs int) {
	ap.flushSilenceForContext(context.Background(), durationMs)
}

func (ap *AudioPipeline) flushSilenceForContext(ctx context.Context, durationMs int) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	if ap.session == nil || ap.session.localTrack == nil {
		return
	}
	sr := ap.session.sampleRate
	if sr == 0 {
		sr = 48000 // default fallback if 0
	}
	sampleCount := (sr * durationMs) / 1000
	samples := make(media.PCM16Sample, sampleCount)
	_ = ap.session.localTrack.WriteSample(samples)

	// Block until the entire RTP queue (including this silence) has been
	// transmitted, so the tts_stop message fires only after the device
	// has finished playing the last word.
	ap.session.localTrack.WaitForPlayout()

	logger.DebugCF("livekit", "Silence flushed – playout complete", map[string]any{
		"session":     ap.sessionKey(),
		"duration_ms": durationMs,
	})
}
