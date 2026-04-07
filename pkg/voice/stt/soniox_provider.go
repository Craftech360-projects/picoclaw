package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// sonioxProvider implements STT using Soniox API
type sonioxProvider struct {
	apiKey string
	model  string
}

// NewSonioxProvider creates a new Soniox provider
func NewSonioxProvider(apiKey, model string) Provider {
	if model == "" {
		model = "standard_v2"
	}
	return &sonioxProvider{apiKey: apiKey, model: model}
}

func (p *sonioxProvider) Name() string                              { return "soniox" }
func (p *sonioxProvider) WithConfig(apiKey, model string) Provider { return NewSonioxProvider(apiKey, model) }
func (p *sonioxProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{"en"}, Models: []string{"standard_v2", "premium_v1_short"},
		SupportsStreaming: false, SupportsDiarization: true, SupportsMultilingual: false,
	}
}

func (p *sonioxProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SONIOX_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("soniox: API key not configured")
	}
	return &sonioxStreamAdapter{
		apiKey: apiKey, model: p.model, sampleRate: opts.SampleRate,
		endpointing: time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer: make([]byte, 0), resultChan: make(chan TranscriptEvent, 10),
		ctx: ctx, mu: sync.Mutex{}, httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type sonioxStreamAdapter struct {
	apiKey, model    string
	sampleRate       int
	endpointing      time.Duration
	audioBuffer      []byte
	resultChan       chan TranscriptEvent
	ctx              context.Context
	mu               sync.Mutex
	closed           bool
	httpClient       *http.Client
}

func (s *sonioxStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock(); defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("stream is closed")
	}
	s.audioBuffer = append(s.audioBuffer, pcm...)
	if s.endpointing > 0 && len(s.audioBuffer) > int(s.endpointing.Seconds()*float64(s.sampleRate*2)) {
		return s.transcribe()
	}
	return nil
}
func (s *sonioxStreamAdapter) Results() <-chan TranscriptEvent { return s.resultChan }
func (s *sonioxStreamAdapter) Finalize() error                 { return s.transcribe() }
func (s *sonioxStreamAdapter) Close() error {
	s.mu.Lock(); defer s.mu.Unlock()
	s.closed = true; close(s.resultChan)
	return nil
}
func (s *sonioxStreamAdapter) transcribe() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}
	wavData, _ := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	req, _ := http.NewRequestWithContext(s.ctx, "POST", "https://api.soniox.com/transcribe-file", bytes.NewReader(wavData))
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.URL.RawQuery = fmt.Sprintf("api_key=%s&include_nonfinal=false&model=%s", s.apiKey, s.model)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Words []struct {
			Text string `json:"text"`
		} `json:"words"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	text := ""
	for _, w := range result.Words {
		text += w.Text + " "
	}
	if text != "" {
		select {
		case s.resultChan <- TranscriptEvent{Text: text, IsFinal: true}:
		default:
		}
	}
	s.audioBuffer = make([]byte, 0)
	return nil
}
