package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"sync"
	"time"
)

// speechmaticsProvider implements STT using Speechmatics API
type speechmaticsProvider struct {
	apiKey   string
	model    string
	language string
}

// NewSpeechmaticsProvider creates a new Speechmatics provider
func NewSpeechmaticsProvider(apiKey, model, language string) Provider {
	if model == "" {
		model = "2.0-a"
	}
	return &speechmaticsProvider{apiKey: apiKey, model: model, language: language}
}

func (p *speechmaticsProvider) Name() string { return "speechmatics" }
func (p *speechmaticsProvider) WithConfig(apiKey, model string) Provider {
	return NewSpeechmaticsProvider(apiKey, model, "")
}
func (p *speechmaticsProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{"en", "es", "fr", "de", "it", "pt", "nl", "auto"},
		Models:    []string{"2.0-a", "2.1-b"},
		SupportsStreaming: false, SupportsDiarization: true, SupportsMultilingual: true,
	}
}

func (p *speechmaticsProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SPEECHMATICS_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("speechmatics: API key not configured")
	}
	return &speechmaticsStreamAdapter{
		apiKey: apiKey, model: p.model, language: opts.Language,
		sampleRate: opts.SampleRate, endpointing: time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer: make([]byte, 0), resultChan: make(chan TranscriptEvent, 10),
		ctx: ctx, mu: sync.Mutex{}, httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

type speechmaticsStreamAdapter struct {
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

func (s *speechmaticsStreamAdapter) SendAudio(pcm []byte) error {
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
func (s *speechmaticsStreamAdapter) Results() <-chan TranscriptEvent { return s.resultChan }
func (s *speechmaticsStreamAdapter) Finalize() error                 { return s.transcribe() }
func (s *speechmaticsStreamAdapter) Close() error {
	s.mu.Lock(); defer s.mu.Unlock()
	s.closed = true; close(s.resultChan)
	return nil
}
func (s *speechmaticsStreamAdapter) transcribe() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}
	wavData, _ := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "audio.wav")
	part.Write(wavData)
	writer.WriteField("type", "transcription")
	writer.Close()

	req, _ := http.NewRequestWithContext(s.ctx, "POST", "https://asr.api.speechmatics.com/v2/jobs/", body)
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		Results struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Results.Alternatives) > 0 {
		select {
		case s.resultChan <- TranscriptEvent{Text: result.Results.Alternatives[0].Transcript, IsFinal: true}:
		default:
		}
	}
	s.audioBuffer = make([]byte, 0)
	return nil
}
