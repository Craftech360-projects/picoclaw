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

// gladiaProvider implements STT using Gladia API
type gladiaProvider struct {
	apiKey string
	model  string
}

// NewGladiaProvider creates a new Gladia provider
func NewGladiaProvider(apiKey, model string) Provider {
	if model == "" {
		model = "gladia-2"
	}
	return &gladiaProvider{apiKey: apiKey, model: model}
}

func (p *gladiaProvider) Name() string                              { return "gladia" }
func (p *gladiaProvider) WithConfig(apiKey, model string) Provider { return NewGladiaProvider(apiKey, model) }
func (p *gladiaProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{"auto"}, Models: []string{"gladia-2"},
		SupportsStreaming: true, SupportsDiarization: true, SupportsMultilingual: true,
	}
}

func (p *gladiaProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("GLADIA_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gladia: API key not configured")
	}
	return &gladiaStreamAdapter{
		apiKey: apiKey, model: p.model, language: opts.Language,
		sampleRate: opts.SampleRate, endpointing: time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer: make([]byte, 0), resultChan: make(chan TranscriptEvent, 10),
		ctx: ctx, mu: sync.Mutex{}, httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type gladiaStreamAdapter struct {
	apiKey, model, language string
	sampleRate              int
	endpointing             time.Duration
	audioBuffer             []byte
	resultChan              chan TranscriptEvent
	ctx                     context.Context
	mu                      sync.Mutex
	closed                  bool
	httpClient              *http.Client
}

func (s *gladiaStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("stream is closed")
	}
	s.audioBuffer = append(s.audioBuffer, pcm...)
	if s.endpointing > 0 && len(s.audioBuffer) > s.endpointingThreshold() {
		return s.transcribe()
	}
	return nil
}
func (s *gladiaStreamAdapter) Results() <-chan TranscriptEvent { return s.resultChan }
func (s *gladiaStreamAdapter) Finalize() error                 { return s.transcribe() }
func (s *gladiaStreamAdapter) Close() error {
	s.mu.Lock(); defer s.mu.Unlock()
	s.closed = true; close(s.resultChan)
	return nil
}
func (s *gladiaStreamAdapter) transcribe() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(s.ctx, "POST", "https://api.gladia.io/audio/text/audio-transcription", bytes.NewReader(wavData))
	req.Header.Set("x-gladia-key", s.apiKey)
	req.Header.Set("Content-Type", "audio/wav")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gladia failed: %s - %s", resp.Status, string(body))
	}
	var result struct {
		Prediction string `json:"prediction"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Prediction != "" {
		select {
		case s.resultChan <- TranscriptEvent{Text: result.Prediction, IsFinal: true}:
		default:
		}
	}
	s.audioBuffer = make([]byte, 0)
	return nil
}
func (s *gladiaStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000
	}
	return int(s.endpointing.Seconds() * float64(s.sampleRate*2))
}
