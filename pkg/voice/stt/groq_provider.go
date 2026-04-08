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

// groqProvider implements STT using Groq's Whisper-compatible API
type groqProvider struct {
	apiKey string
	model  string
}

// NewGroqProvider creates a new Groq Whisper provider
func NewGroqProvider(apiKey, model string) Provider {
	if model == "" {
		model = "whisper-large-v3"
	}
	return &groqProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *groqProvider) Name() string { return "groq" }

func (p *groqProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "it", "ja", "ko", "pt", "ru", "zh", "auto"},
		Models:               []string{"whisper-large-v3", "whisper-large-v3-turbo"},
		SupportsStreaming:    false, // Groq API is non-streaming, we use adapter
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *groqProvider) WithConfig(apiKey, model string) Provider {
	return NewGroqProvider(apiKey, model)
}

func (p *groqProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("GROQ_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("groq: API key not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	// Groq uses OpenAI-compatible API
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.groq.com/openai/v1"
	client := openai.NewClientWithConfig(config)

	// Create streaming adapter for non-streaming Groq API
	stream := &groqStreamAdapter{
		client:       client,
		model:        model,
		language:     normalizeGroqLang(opts.Language),
		sampleRate:   opts.SampleRate,
		interim:      opts.InterimResults,
		endpointing:  opts.EndpointingMS,
		audioBuffer:  make([]byte, 0),
		resultChan:   make(chan TranscriptEvent, 10),
		ctx:          ctx,
		mu:           sync.Mutex{},
	}

	return stream, nil
}

// groqStreamAdapter buffers audio and sends it to Groq's non-streaming API
type groqStreamAdapter struct {
	client       *openai.Client
	model        string
	language     string
	sampleRate   int
	interim      bool
	endpointing  int
	audioBuffer  []byte
	resultChan   chan TranscriptEvent
	ctx          context.Context
	mu           sync.Mutex
	closed       bool
}

func (s *groqStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	// Buffer the audio data
	s.audioBuffer = append(s.audioBuffer, pcm...)

	return nil
}

func (s *groqStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *groqStreamAdapter) Finalize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushBuffer()
}

func (s *groqStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

// flushBuffer sends buffered audio to Groq API and emits results
func (s *groqStreamAdapter) flushBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create WAV file from PCM data
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Prepare transcription request
	// Groq supports whisper-large-v3 and whisper-large-v3-turbo
	// Using string literal as OpenAI SDK may not have latest Groq models
	model := s.model
	if model == "" {
		model = "whisper-large-v3"
	}
	req := openai.AudioRequest{
		Model:    model,
		FilePath: "audio.wav",
		Reader:   bytes.NewReader(wavData),
		Language: s.language,
	}

	// Call Groq API
	resp, err := s.client.CreateTranscription(s.ctx, req)
	if err != nil {
		return fmt.Errorf("groq transcription: %w", err)
	}

	// Send result
	if resp.Text != "" {
		event := TranscriptEvent{
			Text:      resp.Text,
			IsFinal:   true,
			Confidence: 0.95, // Groq doesn't provide confidence
			Language:  s.language,
			Duration:  s.calculateDuration(),
		}

		select {
		case s.resultChan <- event:
		default:
			// Channel full, drop result
		}
	}

	// Clear buffer
	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *groqStreamAdapter) calculateDuration() float64 {
	// Calculate duration from buffer size
	// Duration = numSamples / sampleRate
	// numBytes = numSamples * 2 (16-bit)
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}

func normalizeGroqLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "", "auto":
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
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "portuguese":
		return "pt"
	case "russian":
		return "ru"
	case "chinese", "mandarin":
		return "zh"
	default:
		if len(lang) == 2 || len(lang) == 5 {
			return lang
		}
		return ""
	}
}

// createWAV creates a minimal WAV file from PCM data
// Removed duplicate WAV creation functions - using shared stt_utils.go
