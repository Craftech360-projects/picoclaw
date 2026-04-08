package stt

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/voice/deepgram"
)

type deepgramProvider struct {
	apiKey string
	model  string
}

func NewDeepgramProvider(apiKey, model string) Provider {
	if model == "" {
		model = "nova-2"
	}
	return &deepgramProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *deepgramProvider) Name() string { return "deepgram" }

func (p *deepgramProvider) WithConfig(apiKey, model string) Provider {
	return NewDeepgramProvider(apiKey, model)
}

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
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("deepgram: API key not configured")
	}

	dg := deepgram.NewDeepgramTranscriber(apiKey)

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	streamOpts := deepgram.StreamOpts{
		SampleRate:     opts.SampleRate,
		Language:       normalizeDeepgramLanguage(opts.Language),
		Model:          model,
		InterimResults: opts.InterimResults,
		EndpointingMS:  opts.EndpointingMS,
		Channels:       opts.Channels,
	}

	stream, err := dg.OpenStream(streamOpts)
	if err != nil {
		return nil, err
	}

	return newDeepgramStreamAdapter(stream), nil
}

// deepgramStreamAdapter wraps the existing Deepgram stream to implement stt.TranscriptionStream.
type deepgramStreamAdapter struct {
	stream deepgram.TranscriptionStream
	out    chan TranscriptEvent
}

func newDeepgramStreamAdapter(stream deepgram.TranscriptionStream) *deepgramStreamAdapter {
	out := make(chan TranscriptEvent, 32)
	go func() {
		defer close(out)
		for evt := range stream.Results() {
			out <- TranscriptEvent{
				Text:        evt.Text,
				IsFinal:     evt.IsFinal,
				SpeechStart: evt.SpeechStart,
				SpeechEnd:   evt.SpeechEnd,
			}
		}
	}()
	return &deepgramStreamAdapter{
		stream: stream,
		out:    out,
	}
}

func (a *deepgramStreamAdapter) SendAudio(pcm []byte) error {
	return a.stream.SendAudio(pcm)
}

func (a *deepgramStreamAdapter) Results() <-chan TranscriptEvent {
	return a.out
}

func (a *deepgramStreamAdapter) Finalize() error {
	return a.stream.Finalize()
}

func (a *deepgramStreamAdapter) Close() error {
	return a.stream.Close()
}

func normalizeDeepgramLanguage(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "", "auto", "multi":
		// Let Deepgram auto-detect by omitting the language param.
		return ""
	case "english":
		return "en"
	case "hindi":
		return "hi"
	case "spanish":
		return "es"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "italian":
		return "it"
	case "portuguese":
		return "pt"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "chinese", "mandarin":
		return "zh"
	default:
		// Keep language codes as-is (e.g. en, hi, en-US).
		if len(lang) == 2 || len(lang) == 5 {
			return lang
		}
		return ""
	}
}
