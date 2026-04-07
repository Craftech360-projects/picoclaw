package stt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

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

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	client := openai.NewClient(apiKey)

	stream := &openaiStreamAdapter{
		client:      client,
		model:       model,
		language:    opts.Language,
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

	// Buffer audio - OpenAI requires at least 0.1 second of audio
	s.audioBuffer = append(s.audioBuffer, pcm...)

	// Transcribe when we have enough audio (minimum 100ms at 16kHz)
	minSize := int(float64(s.sampleRate) * 2 * 0.1) // 0.1 second in bytes
	if len(s.audioBuffer) >= minSize {
		return s.flushBuffer()
	}

	return nil
}

func (s *openaiStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *openaiStreamAdapter) Finalize() error {
	return s.flushBuffer()
}

func (s *openaiStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

		select {
		case s.resultChan <- event:
		default:
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
