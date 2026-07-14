package tts

import (
	"context"
	"io"
)

// AudioStream reads synthesized audio chunks.
type AudioStream interface {
	Read() ([]byte, error)
	Close() error
}

// Provider defines a streaming TTS provider.
type Provider interface {
	Synthesize(ctx context.Context, text string) (AudioStream, error)
}

// BufferStream adapts a fully-buffered PCM blob (from a non-streaming/batch TTS
// backend) to the streaming AudioStream interface: it yields the buffer once,
// then io.EOF.
type BufferStream struct {
	data []byte
	done bool
}

// NewBufferStream wraps a single PCM buffer as an AudioStream.
func NewBufferStream(data []byte) *BufferStream { return &BufferStream{data: data} }

// Read returns the buffer on the first call, then io.EOF.
func (s *BufferStream) Read() ([]byte, error) {
	if s.done || len(s.data) == 0 {
		return nil, io.EOF
	}
	s.done = true
	return s.data, nil
}

// Close is a no-op; there is no underlying resource.
func (s *BufferStream) Close() error { return nil }
