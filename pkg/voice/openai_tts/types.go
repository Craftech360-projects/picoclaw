package openai_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures an OpenAI-compatible TTS endpoint (OpenAI, speaches, etc.).
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	SampleRateHz int
	BaseURL      string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

var _ tts.Provider = (*OpenAITTS)(nil)
