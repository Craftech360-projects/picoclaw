package cartesia_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures Cartesia streaming TTS.
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	SampleRateHz int
	Language     string
	BaseURL      string
	APIVersion   string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

// TTSProvider defines a streaming TTS provider.
type TTSProvider = tts.Provider

var _ tts.Provider = (*CartesiaTTS)(nil)
