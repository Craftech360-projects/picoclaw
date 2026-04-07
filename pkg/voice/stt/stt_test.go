package stt

import (
	"context"
	"testing"
)

func TestProviderCapabilities_NotEmpty(t *testing.T) {
	caps := ProviderCapabilities{
		Languages:           []string{"en", "es"},
		Models:              []string{"nova-2"},
		SupportsStreaming:   true,
		SupportsDiarization: false,
		SupportsMultilingual: true,
	}

	if len(caps.Languages) == 0 {
		t.Error("Expected non-empty Languages")
	}
	if len(caps.Models) == 0 {
		t.Error("Expected non-empty Models")
	}
	if !caps.SupportsStreaming {
		t.Error("Expected SupportsStreaming to be true")
	}
	if caps.SupportsDiarization {
		t.Error("Expected SupportsDiarization to be false")
	}
	if !caps.SupportsMultilingual {
		t.Error("Expected SupportsMultilingual to be true")
	}
}

func TestTranscriptEvent_Fields(t *testing.T) {
	evt := TranscriptEvent{
		Text:        "hello world",
		IsFinal:     true,
		SpeechStart: true,
		SpeechEnd:   false,
		Confidence:  0.95,
		Language:    "en",
		Duration:    1.5,
	}

	if evt.Text != "hello world" {
		t.Errorf("Expected Text 'hello world', got '%s'", evt.Text)
	}
	if !evt.IsFinal {
		t.Error("Expected IsFinal to be true")
	}
	if !evt.SpeechStart {
		t.Error("Expected SpeechStart to be true")
	}
	if evt.SpeechEnd {
		t.Error("Expected SpeechEnd to be false")
	}
	if evt.Confidence != 0.95 {
		t.Errorf("Expected Confidence 0.95, got %f", evt.Confidence)
	}
	if evt.Language != "en" {
		t.Errorf("Expected Language 'en', got '%s'", evt.Language)
	}
	if evt.Duration != 1.5 {
		t.Errorf("Expected Duration 1.5, got %f", evt.Duration)
	}
}

func TestStreamOptions_Defaults(t *testing.T) {
	opts := StreamOptions{
		SampleRate:     16000,
		Channels:       1,
		Language:       "en",
		Model:          "nova-2",
		InterimResults: true,
		EndpointingMS:  800,
	}

	if opts.SampleRate != 16000 {
		t.Errorf("Expected SampleRate 16000, got %d", opts.SampleRate)
	}
	if opts.Channels != 1 {
		t.Errorf("Expected Channels 1, got %d", opts.Channels)
	}
	if opts.Language != "en" {
		t.Errorf("Expected Language 'en', got '%s'", opts.Language)
	}
	if opts.Model != "nova-2" {
		t.Errorf("Expected Model 'nova-2', got '%s'", opts.Model)
	}
}

// mockProvider is a simple implementation for testing the interface contract
type mockProvider struct {
	name string
	caps ProviderCapabilities
}

func (m *mockProvider) Name() string                           { return m.name }
func (m *mockProvider) Capabilities() ProviderCapabilities     { return m.caps }
func (m *mockProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	return nil, nil
}

func TestMockProvider_ImplementsInterface(t *testing.T) {
	var _ Provider = (*mockProvider)(nil)
}
