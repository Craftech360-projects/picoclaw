package livekit

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/livekit/media-sdk"
	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
	"github.com/sipeed/picoclaw/pkg/voice/elevenlabs_tts"
)

// sentenceSplitter accumulates text and emits complete sentences.
type sentenceSplitter struct {
	buf strings.Builder
}

func newSentenceSplitter() *sentenceSplitter {
	return &sentenceSplitter{}
}

func (s *sentenceSplitter) Feed(r rune) string {
	s.buf.WriteRune(r)
	if r == '.' || r == '!' || r == '?' {
		sentence := s.buf.String()
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
	tts     *elevenlabs_tts.ElevenLabsTTS
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge, tts *elevenlabs_tts.ElevenLabsTTS) *AudioPipeline {
	return &AudioPipeline{
		session: session,
		bridge:  bridge,
		tts:     tts,
	}
}

// HandleUtterance processes a complete user utterance: calls the agent and speaks the response.
func (ap *AudioPipeline) HandleUtterance(ctx context.Context, sessionKey string, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if ap.bridge == nil {
		return fmt.Errorf("agent bridge is nil")
	}

	splitter := newSentenceSplitter()

	err := ap.bridge.ChatStream(ctx, sessionKey, text, func(chunk string) {
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				ap.synthesizeAndPlay(ctx, sentence)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	if remainder := splitter.Flush(); remainder != "" {
		ap.synthesizeAndPlay(ctx, remainder)
	}
	return nil
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
				ap.cancelTTS()
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

				sessionKey := ap.sessionKey()
				if sessionKey == "" {
					continue
				}

				ttsCtx, ttsCancel := context.WithCancel(ctx)
				ap.setTTSCancel(ttsCancel)

				go func() {
					defer ttsCancel()
					_ = ap.HandleUtterance(ttsCtx, sessionKey, text)
				}()
			}
		}
	}
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	stream, err := ap.tts.Synthesize(ctx, text)
	if err != nil {
		return
	}
	defer stream.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		chunk, err := stream.Read()
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
		if len(chunk) == 0 {
			continue
		}

		samples := bytesToPCM16(chunk)
		if len(samples) == 0 {
			continue
		}
		_ = ap.session.localTrack.WriteSample(samples)
	}
}

func (ap *AudioPipeline) cancelTTS() {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.ttsCancel != nil {
		ps.ttsCancel()
		ps.ttsCancel = nil
	}
	if ap.session.localTrack != nil {
		ap.session.localTrack.ClearQueue()
	}
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
