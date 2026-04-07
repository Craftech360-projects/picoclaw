package stt

import (
	"context"
	"fmt"
	"os"

	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
)

type deepgramProvider struct{}

func (p *deepgramProvider) Name() string { return "deepgram" }

func (p *deepgramProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "multi"},
		Models:               []string{"nova-3", "nova-2", "flux"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *deepgramProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := os.Getenv("DEEPGRAM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("deepgram: API key not configured")
	}

	dg := deepgram.NewDeepgramTranscriber(apiKey)

	streamOpts := deepgram.StreamOpts{
		SampleRate:     opts.SampleRate,
		Language:       opts.Language,
		Model:          opts.Model,
		InterimResults: opts.InterimResults,
		EndpointingMS:  opts.EndpointingMS,
		Channels:       opts.Channels,
	}

	stream, err := dg.OpenStream(streamOpts)
	if err != nil {
		return nil, err
	}

	return &deepgramStreamAdapter{stream: stream}, nil
}

// deepgramStreamAdapter wraps the existing Deepgram stream to implement stt.TranscriptionStream.
type deepgramStreamAdapter struct {
	stream deepgram.TranscriptionStream
}

func (a *deepgramStreamAdapter) SendAudio(pcm []byte) error {
	return a.stream.SendAudio(pcm)
}

func (a *deepgramStreamAdapter) Results() <-chan TranscriptEvent {
	out := make(chan TranscriptEvent, 10)
	go func() {
		defer close(out)
		for evt := range a.stream.Results() {
			out <- TranscriptEvent{
				Text:        evt.Text,
				IsFinal:     evt.IsFinal,
				SpeechStart: evt.SpeechStart,
				SpeechEnd:   evt.SpeechEnd,
			}
		}
	}()
	return out
}

func (a *deepgramStreamAdapter) Finalize() error {
	return a.stream.Finalize()
}

func (a *deepgramStreamAdapter) Close() error {
	return a.stream.Close()
}
