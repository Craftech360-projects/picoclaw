package tts

import "context"

// AudioStream reads synthesized audio chunks.
type AudioStream interface {
	Read() ([]byte, error)
	Close() error
}

// Provider defines a streaming TTS provider.
type Provider interface {
	Synthesize(ctx context.Context, text string) (AudioStream, error)
}
