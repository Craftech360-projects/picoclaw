package elevenlabs_tts

import "context"

// TTSConfig configures ElevenLabs streaming TTS.
type TTSConfig struct {
	APIKey       string
	VoiceID      string
	ModelID      string
	OutputFormat string
	BaseURL      string
}

// AudioStream reads synthesized audio chunks.
type AudioStream interface {
	Read() ([]byte, error)
	Close() error
}

// TTSProvider defines a streaming TTS provider.
type TTSProvider interface {
	Synthesize(ctx context.Context, text string) (AudioStream, error)
}
