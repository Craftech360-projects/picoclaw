package stt

import (
	"context"
)

// Provider defines a speech-to-text provider.
type Provider interface {
	// Name returns the provider identifier (e.g., "deepgram", "groq")
	Name() string

	// Capabilities returns supported features
	Capabilities() ProviderCapabilities

	// OpenStream starts a streaming transcription session.
	OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error)
}

// ProviderCapabilities describes what the provider supports
type ProviderCapabilities struct {
	Languages            []string // Supported language codes
	Models               []string // Available model IDs
	SupportsStreaming    bool
	SupportsDiarization  bool
	SupportsMultilingual bool
}

// TranscriptionStream is a bidirectional audio stream with transcription results.
type TranscriptionStream interface {
	// SendAudio sends PCM audio data (16-bit little-endian).
	SendAudio(pcm []byte) error

	// Results returns a channel of transcription events.
	Results() <-chan TranscriptEvent

	// Finalize signals end of utterance to flush pending results.
	Finalize() error

	// Close closes the stream.
	Close() error
}

// TranscriptEvent represents a transcription result.
type TranscriptEvent struct {
	Text        string
	IsFinal     bool
	SpeechStart bool
	SpeechEnd   bool
	Confidence  float64
	Language    string
	Duration    float64
}

// StreamOptions configures a transcription stream.
type StreamOptions struct {
	SampleRate     int
	Channels       int
	Language       string
	Model          string
	InterimResults bool
	EndpointingMS  int
}
