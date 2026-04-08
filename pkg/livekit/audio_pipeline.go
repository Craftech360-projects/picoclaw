package livekit

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/livekit/media-sdk"
	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

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
	session         *RoomSession
	bridge          *AgentBridge
	tts             tts.Provider
	vadEvent        <-chan interface{}
	primaryLanguage string // used for language-aware error fallback phrases
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge, tts tts.Provider, vadEvent <-chan interface{}) *AudioPipeline {
	var lang string
	if session != nil {
		lang = session.primaryLanguage
	}
	return &AudioPipeline{
		session:         session,
		bridge:          bridge,
		tts:             tts,
		vadEvent:        vadEvent,
		primaryLanguage: lang,
	}
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
	firstChunkReceived := false
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
		if !firstChunkReceived {
			firstChunkReceived = true
			if ap.session != nil {
				ap.session.PublishAgentState("thinking", "speaking")
				ap.session.PublishSpeechCreated("")
			}
			if fillerCancel != nil {
				fillerCancel()                       // Cancel filler if LLM starts responding quickly
				ap.cancelTTS("llm_response_started") // clear any buffered filler audio
			}
		}
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				ap.synthesizeAndPlay(ctx, sentence)
			}
		}
	}
	onDoneCallback := func() {
		// onDone is called when the LLM turn is finished (including async tools)
		if fillerCancel != nil {
			fillerCancel()
		}
		if remainder := splitter.Flush(); remainder != "" {
			ap.synthesizeAndPlay(ctx, remainder)
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
		logger.WarnCF("livekit", "ChatStream failed, retrying", map[string]any{
			"attempt": attempt + 1,
			"max":     maxRetries,
			"error":   err.Error(),
			"session": sessionKey,
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

// TriggerGreeting executes a proactive dynamic LLM greeting using the bridge.
// It bypasses the user speech wait loop and talks directly into the TTS pipeline.

func (ap *AudioPipeline) TriggerGreeting(ctx context.Context, sessionKey string) {
	if ap.bridge == nil || ap.session == nil {
		return
	}

	logger.InfoCF("livekit", "Triggering dynamic agent greeting", map[string]any{
		"session": sessionKey,
	})

	ap.session.PublishAgentState("listening", "thinking")
	firstChunkReceived := false
	splitter := newSentenceSplitter()

	go func() {
		err := ap.bridge.GenerateGreeting(ctx, sessionKey, func(chunk string) {
			if !firstChunkReceived {
				firstChunkReceived = true
				ap.session.PublishAgentState("thinking", "speaking")
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeAndPlay(ctx, sentence)
				}
			}
		}, func() {
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeAndPlay(ctx, remainder)
			}
			ap.flushSilence(500)
			ap.session.PublishAgentState("speaking", "listening")
		})

		if err != nil {
			logger.ErrorCF("livekit", "Failed to generate dynamic greeting", map[string]any{
				"session": sessionKey,
				"error":   err.Error(),
			})
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
	var latestTranscript string
	var finalizeTimer *time.Timer
	var finalizeTimerC <-chan time.Time

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
		stopFinalizeTimer()

		if ap.session != nil && ap.session.participant != nil {
			ap.session.participant.speaking.Store(false)
		}

		if text == "" {
			return
		}
		logger.DebugCF("livekit", "Speech end with text", map[string]any{
			"session": ap.sessionKey(),
			"text":    text,
			"trigger": trigger,
		})

		sessionKey := ap.sessionKey()
		if sessionKey == "" {
			return
		}

		ttsCtx, ttsCancel := context.WithCancel(context.Background())
		ap.setTTSCancel(ttsCancel)

		go func() {
			_, _ = ap.HandleUtterance(ttsCtx, sessionKey, text, ttsCancel)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			stopFinalizeTimer()
			return

		case vadEvt, ok := <-ap.vadEvent:
			if !ok {
				ap.vadEvent = nil
				continue
			}
			if evt, ok := vadEvt.(vad.VADEvent); ok {
				if evt.SpeechStart {
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
					ap.cancelTTS("vad_speech_start")
					stopFinalizeTimer()
					vadSpeechEnded = false
				}

				if evt.SpeechEnd {
					vadSpeechEnded = true
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
			}

			if evt.IsFinal && evt.Text != "" {
				utterance.WriteString(evt.Text)
				utterance.WriteString(" ")
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
	logger.InfoCF("livekit", "Triggering spontaneous announcement for background task", map[string]any{
		"tool":    evt.ToolName,
		"session": sessionKey,
	})

	ttsCtx, ttsCancel := context.WithCancel(context.Background())
	ap.setTTSCancel(ttsCancel)

	splitter := newSentenceSplitter()

	go func() {
		if ap.session != nil {
			ap.session.PublishAgentState("listening", "thinking")
		}
		firstChunkReceived := false

		err := ap.bridge.GenerateSpontaneousResponse(ttsCtx, sessionKey, evt, func(chunk string) {
			if !firstChunkReceived {
				firstChunkReceived = true
				if ap.session != nil {
					ap.session.PublishAgentState("thinking", "speaking")
					ap.session.PublishSpeechCreated("")
				}
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeAndPlay(ttsCtx, sentence)
				}
			}
		}, func() {
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeAndPlay(ttsCtx, remainder)
			}
			ap.flushSilence(500) // trailing silence for spontaneous replies
			if ap.session != nil {
				ap.session.PublishAgentState("speaking", "listening")
			}
			ttsCancel()
		})
		if err != nil {
			logger.ErrorCF("livekit", "Spontaneous response generation failed", map[string]any{
				"error":   err.Error(),
				"tool":    evt.ToolName,
				"session": sessionKey,
			})
			ttsCancel()
		}
	}()
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	logger.DebugCF("livekit", "Synthesizing audio chunk", map[string]any{
		"session": ap.sessionKey(),
		"text":    text,
	})
	stream, err := ap.tts.Synthesize(ctx, text)
	if err != nil {
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

	started := time.Now()
	wroteAudio := false
	chunksWritten := 0
	samplesWritten := 0
	for {
		select {
		case <-ctx.Done():
			logger.WarnCF("livekit", "Audio stream canceled", map[string]any{
				"session":         ap.sessionKey(),
				"track_sid":       ap.localTrackSID(),
				"chunks_written":  chunksWritten,
				"samples_written": samplesWritten,
				"duration_ms":     time.Since(started).Milliseconds(),
			})
			return
		default:
		}

		chunk, err := stream.Read()
		if err == io.EOF {
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

func (ap *AudioPipeline) cancelTTS(reason string) {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	hadActiveTTS := ps.ttsCancel != nil
	if ps.ttsCancel != nil {
		ps.ttsCancel()
		ps.ttsCancel = nil
	}
	if ap.session.localTrack != nil {
		ap.session.localTrack.ClearQueue()
	}

	if hadActiveTTS {
		ap.session.PublishAgentState("speaking", "listening")
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
	ps.mu.Unlock()
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
