package azure_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures Azure Cognitive Services text-to-speech.
type TTSConfig struct {
	APIKey  string
	VoiceID string
	// Endpoint is the full synthesis endpoint, e.g.
	// https://<region>.tts.speech.microsoft.com/cognitiveservices/v1
	Endpoint     string
	SampleRateHz int
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

var _ tts.Provider = (*AzureTTS)(nil)
