package smallest_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures SmallestAI Waves streaming TTS.
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	OutputFormat string
	SampleRateHz int
	BaseURL      string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

// TTSProvider defines a streaming TTS provider.
type TTSProvider = tts.Provider

var _ tts.Provider = (*SmallestTTS)(nil)
