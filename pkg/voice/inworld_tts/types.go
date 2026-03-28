package inworld_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures Inworld streaming TTS.
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	SampleRateHz int
	Temperature  float64
	BaseURL      string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

// TTSProvider defines a streaming TTS provider.
type TTSProvider = tts.Provider

var _ tts.Provider = (*InworldTTS)(nil)
