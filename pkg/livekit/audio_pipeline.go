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
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
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
	s.buf.WriteRune(r)
	text := s.buf.String()

	// Only attempt to split if we have a potential sentence or clause boundary
	if r == '.' || r == '!' || r == '?' || r == '\n' || r == ',' || r == ';' || r == ':' {
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
				if len(text) > 15 {
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
	session *RoomSession
	bridge  *AgentBridge
	tts     tts.Provider
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge, tts tts.Provider) *AudioPipeline {
	return &AudioPipeline{
		session: session,
		bridge:  bridge,
		tts:     tts,
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

	// Play filler word asynchronously so we can cancel it if LLM is fast
	if ap.session != nil && len(ap.session.fillerWords) > 0 {
		fillerCtx, fillerCancel = context.WithCancel(ctx)
		filler := ap.session.fillerWords[rand.Intn(len(ap.session.fillerWords))]
		go func() {
			ap.synthesizeAndPlay(fillerCtx, filler)
		}()
	}

	asyncPending, err := ap.bridge.ChatStream(ctx, sessionKey, text, func(chunk string) {
		if !firstChunkReceived {
			firstChunkReceived = true
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
	}, func() {
		// onDone is called when the LLM turn is finished (including async tools)
		if fillerCancel != nil {
			fillerCancel()
		}
		if remainder := splitter.Flush(); remainder != "" {
			ap.synthesizeAndPlay(ctx, remainder)
		}
		if onDone != nil {
			onDone()
		}
	})

	if err != nil {
		// ChatStream will have called onDone already
		return false, fmt.Errorf("agent: %w", err)
	}

	// If a tool is running asynchronously, we don't clear the context immediately.
	// The AgentBridge will call onDone when all iterations are complete.
	return asyncPending, nil
}

// RunInbound reads Deepgram transcription events and calls the agent on speech end.
func (ap *AudioPipeline) RunInbound(ctx context.Context, dgStream deepgram.TranscriptionStream) {
	if dgStream == nil {
		return
	}
	var utterance strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-dgStream.Results():
			if !ok {
				return
			}

			if evt.SpeechStart {
				if ap.session != nil && ap.session.participant != nil {
					ap.session.participant.speaking.Store(true)
				}
				logger.DebugCF("livekit", "Speech start", map[string]any{
					"session": ap.sessionKey(),
				})
				ap.cancelTTS("speech_start")
			}

			if evt.IsFinal && evt.Text != "" {
				utterance.WriteString(evt.Text)
				utterance.WriteString(" ")
			}

			if evt.SpeechEnd {
				text := strings.TrimSpace(utterance.String())
				utterance.Reset()

				if ap.session != nil && ap.session.participant != nil {
					ap.session.participant.speaking.Store(false)
				}

				if text == "" {
					continue
				}
				logger.DebugCF("livekit", "Speech end", map[string]any{
					"session": ap.sessionKey(),
					"text":    text,
				})

				sessionKey := ap.sessionKey()
				if sessionKey == "" {
					continue
				}

				// We create a new context for the TTS synthesis and playback, but we also want it to be cancelable
				// if new speech starts. However, we don't want it to be tied to the loop's context for closure,
				// because it might need to outlive this iteration (e.g. during async tool calls).
				// We use a context that is cancelable but rooted in context.Background() or a session-level context.
				ttsCtx, ttsCancel := context.WithCancel(context.Background())
				ap.setTTSCancel(ttsCancel)

				go func() {
					// HandleUtterance will call ttsCancel (via onDone) when it's completely finished,
					// including any asynchronous tool iterations.
					_, _ = ap.HandleUtterance(ttsCtx, sessionKey, text, ttsCancel)
				}()
			}
		}
	}
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	logger.DebugCF("livekit", "TTS start", map[string]any{
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

	logger.DebugCF("livekit", "TTS forward start", map[string]any{
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
			logger.WarnCF("livekit", "TTS forward canceled", map[string]any{
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
			logger.DebugCF("livekit", "TTS forward complete", map[string]any{
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
	if ap.session == nil || ap.session.participant == nil || ap.session.roomInfo == nil {
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
