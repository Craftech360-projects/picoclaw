package edge_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures Microsoft Edge (free, keyless) text-to-speech.
type TTSConfig struct {
	VoiceID string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

var _ tts.Provider = (*EdgeTTS)(nil)
