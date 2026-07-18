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
	// Temperature (0.01–1.0) controls bulbul:v3 expressiveness; 0 -> default.
	Temperature float64
	// OutputBitrate (e.g. "128k") applies to compressed codecs only; ignored
	// for linear16. Empty -> default.
	OutputBitrate string
	BaseURL       string
}

// AudioStream reads synthesized audio chunks.
type AudioStream = tts.AudioStream

var _ tts.Provider = (*SarvamTTS)(nil)
