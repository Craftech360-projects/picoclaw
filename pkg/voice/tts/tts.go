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

// emotionKey carries the per-sentence emotion tag (e.g. "happy", "sleepy") from
// the pipeline to providers that can shade prosody. Context-based so the
// Provider interface stays unchanged; providers without emotion support ignore it.
type emotionKey struct{}

// WithEmotion returns a context carrying the emotion tag for one synthesis call.
func WithEmotion(ctx context.Context, emotion string) context.Context {
	if emotion == "" {
		return ctx
	}
	return context.WithValue(ctx, emotionKey{}, emotion)
}

// EmotionFromContext returns the emotion tag set by WithEmotion, or "".
func EmotionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(emotionKey{}).(string); ok {
		return v
	}
	return ""
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
