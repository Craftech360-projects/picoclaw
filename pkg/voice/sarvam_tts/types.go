package sarvam_tts

import "github.com/sipeed/picoclaw/pkg/voice/tts"

// TTSConfig configures Sarvam (bulbul) text-to-speech.
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	SampleRateHz int
	// LanguageCode is the resolved Sarvam language code (e.g. "hi-IN"), derived
	// from the session language via ResolveLanguageCode.
	LanguageCode string
	BaseURL      string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

var _ tts.Provider = (*SarvamTTS)(nil)
