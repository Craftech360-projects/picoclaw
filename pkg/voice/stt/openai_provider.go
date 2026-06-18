package stt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

// newOpenAISTTClient builds a go-openai client, honoring OPENAI_BASE_URL so the
// same provider can target a local OpenAI-compatible STT server (e.g. speaches).
// ponytail: env-var override; promote to per-provider config if multi-endpoint needed.
func newOpenAISTTClient(apiKey string) *openai.Client {
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		return openai.NewClient(apiKey)
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	return openai.NewClientWithConfig(cfg)
}

// openaiProvider implements STT using OpenAI Whisper API
type openaiProvider struct {
	apiKey string
	model  string
}

// NewOpenAIProvider creates a new OpenAI Whisper provider
func NewOpenAIProvider(apiKey, model string) Provider {
	if model == "" {
		model = "whisper-1"
	}
	return &openaiProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *openaiProvider) Name() string { return "openai" }

func (p *openaiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "it", "ja", "ko", "pt", "ru", "zh", "auto"},
		Models:               []string{"whisper-1"},
		SupportsStreaming:    false, // OpenAI Whisper API is non-streaming
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *openaiProvider) WithConfig(apiKey, model string) Provider {
	return NewOpenAIProvider(apiKey, model)
}

func (p *openaiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("openai: API key not configured")
	}

	// Model is DB-driven (stt_providers.model via WithConfig / opts.Model).
	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}
	// Language arrives from the session and is often "auto", which mis-detects accented
	// English. There is no DB path for STT language, so fall back to English when unset/auto.
	lang := opts.Language
	if lang == "" || strings.EqualFold(lang, "auto") {
		lang = "en"
	}

	client := newOpenAISTTClient(apiKey)

	stream := &openaiStreamAdapter{
		client:      client,
		model:       model,
		language:    lang,
		sampleRate:  opts.SampleRate,
		audioBuffer: make([]byte, 0),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		mu:          sync.Mutex{},
	}

	return stream, nil
}

// openaiStreamAdapter buffers audio and sends it to OpenAI Whisper API
type openaiStreamAdapter struct {
	client      *openai.Client
	model       string
	language    string
	sampleRate  int
	audioBuffer []byte
	resultChan  chan TranscriptEvent
	ctx         context.Context
	mu          sync.Mutex
	closed      bool
}

func (s *openaiStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	// Whisper is batch, not streaming: accumulate the whole utterance and transcribe
	// once on Finalize. Flushing every 100ms sends sub-second fragments that the
	// server rejects (HTTP 500) and wastes requests.
	s.audioBuffer = append(s.audioBuffer, pcm...)
	return nil
}

func (s *openaiStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *openaiStreamAdapter) Finalize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushBuffer()
}

func (s *openaiStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		// Close is called from both the disconnect handler and track cleanup;
		// guard against double-close panicking on resultChan.
		return nil
	}
	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *openaiStreamAdapter) flushBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create WAV file
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Prepare transcription request
	req := openai.AudioRequest{
		Model:    s.model,
		FilePath: "audio.wav",
		Reader:   bytes.NewReader(wavData),
		Language: s.language,
	}

	// Call OpenAI API
	resp, err := s.client.CreateTranscription(s.ctx, req)
	if err != nil {
		return fmt.Errorf("openai transcription: %w", err)
	}

	// Send result
	if resp.Text != "" {
		event := TranscriptEvent{
			Text:     resp.Text,
			IsFinal:  true,
			Language: s.language,
			Duration: s.calculateDuration(),
		}

		if !s.closed {
			select {
			case s.resultChan <- event:
			default:
			}
		}
	}

	// Clear buffer
	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *openaiStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}
