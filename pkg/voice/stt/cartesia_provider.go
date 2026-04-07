package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// cartesiaProvider implements STT using Cartesia Ink Whisper API
type cartesiaProvider struct {
	apiKey string
	model  string
}

// NewCartesiaProvider creates a new Cartesia provider
func NewCartesiaProvider(apiKey, model string) Provider {
	if model == "" {
		model = "ink-whisper"
	}
	return &cartesiaProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *cartesiaProvider) Name() string { return "cartesia" }

func (p *cartesiaProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"}, // Ink Whisper auto-detects 100+ languages
		Models:               []string{"ink-whisper"},
		SupportsStreaming:    false, // Using REST API
		SupportsDiarization:  false,
		SupportsMultilingual: true, // 100+ languages
	}
}

func (p *cartesiaProvider) WithConfig(apiKey, model string) Provider {
	return NewCartesiaProvider(apiKey, model)
}

func (p *cartesiaProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("CARTESIA_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("cartesia: API key not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	stream := &cartesiaStreamAdapter{
		apiKey:      apiKey,
		model:       model,
		sampleRate:  opts.SampleRate,
		endpointing: time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer: make([]byte, 0),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		mu:          sync.Mutex{},
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	return stream, nil
}

// cartesiaStreamAdapter buffers audio and sends to Cartesia API
type cartesiaStreamAdapter struct {
	apiKey      string
	model       string
	sampleRate  int
	endpointing time.Duration
	audioBuffer []byte
	resultChan  chan TranscriptEvent
	ctx         context.Context
	mu          sync.Mutex
	closed      bool
	httpClient  *http.Client
}

func (s *cartesiaStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	s.audioBuffer = append(s.audioBuffer, pcm...)

	if s.endpointing > 0 && len(s.audioBuffer) > s.endpointingThreshold() {
		return s.transcribeBuffer()
	}

	return nil
}

func (s *cartesiaStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *cartesiaStreamAdapter) Finalize() error {
	return s.transcribeBuffer()
}

func (s *cartesiaStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *cartesiaStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create WAV file
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Cartesia transcription endpoint
	url := "https://api.cartesia.ai/tts/audio/transcribe"
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(wavData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-API-Key", s.apiKey)
	req.Header.Set("Content-Type", "audio/wav")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transcription failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	// Send event
	if result.Text != "" {
		event := TranscriptEvent{
			Text:     result.Text,
			IsFinal:  true,
			Language: result.Language,
			Duration: s.calculateDuration(),
		}

		select {
		case s.resultChan <- event:
		default:
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *cartesiaStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000 // ~1 second at 16kHz mono 16-bit
	}
	bytesPerSecond := s.sampleRate * 2
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}

func (s *cartesiaStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}
