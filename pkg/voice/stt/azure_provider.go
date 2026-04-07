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

// azureProvider implements STT using Azure Cognitive Services Speech API
type azureProvider struct {
	apiKey   string
	region   string
	model    string
	language string
}

// NewAzureProvider creates a new Azure Speech provider
func NewAzureProvider(apiKey, region, model, language string) Provider {
	if model == "" {
		model = "latest"
	}
	return &azureProvider{
		apiKey:   apiKey,
		region:   region,
		model:    model,
		language: language,
	}
}

func (p *azureProvider) Name() string { return "azure" }

func (p *azureProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "it", "ja", "ko", "pt", "ru", "zh", "ar", "auto"},
		Models:               []string{"latest", "baseline", "conversation"},
		SupportsStreaming:    false, // Using REST API
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *azureProvider) WithConfig(apiKey, model string) Provider {
	return NewAzureProvider(apiKey, "", model, "")
}

func (p *azureProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_SPEECH_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("azure speech: API key not configured")
	}

	region := p.region
	if region == "" {
		region = os.Getenv("AZURE_SPEECH_REGION")
	}
	if region == "" {
		return nil, fmt.Errorf("azure speech: region not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	language := opts.Language
	if language == "" && p.language != "" {
		language = p.language
	}

	stream := &azureStreamAdapter{
		apiKey:      apiKey,
		region:      region,
		model:       model,
		language:    language,
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

// azureStreamAdapter handles Azure Speech transcription
type azureStreamAdapter struct {
	apiKey      string
	region      string
	model       string
	language    string
	sampleRate  int
	endpointing time.Duration
	audioBuffer []byte
	resultChan  chan TranscriptEvent
	ctx         context.Context
	mu          sync.Mutex
	closed      bool
	httpClient  *http.Client
}

func (s *azureStreamAdapter) SendAudio(pcm []byte) error {
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

func (s *azureStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *azureStreamAdapter) Finalize() error {
	return s.transcribeBuffer()
}

func (s *azureStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *azureStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create WAV file
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Build Azure Speech API URL
	url := fmt.Sprintf("https://%s.stt.speech.microsoft.com/speech/recognition/conversation/cognitiveservices/v1", s.region)
	if s.model == "conversation" {
		url = fmt.Sprintf("https://%s.stt.speech.microsoft.com/speech/recognition/conversation/cognitiveservices/v1", s.region)
	} else if s.model == "baseline" {
		url = fmt.Sprintf("https://%s.stt.speech.microsoft.com/speech/recognition/interactive/cognitiveservices/v1", s.region)
	}

	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(wavData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Ocp-Apim-Subscription-Key", s.apiKey)
	req.Header.Set("Content-Type", fmt.Sprintf("audio/wav; codecs=audio/pcm; samplerate=%d", s.sampleRate))

	if s.language != "" && s.language != "auto" {
		req.Header.Set("Accept-Language", s.language)
	}

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
		RecognitionStatus string  `json:"RecognitionStatus"`
		DisplayText       string  `json:"DisplayText"`
		Offset            int64   `json:"Offset"`
		Duration          int64   `json:"Duration"`
		Confidence        float64 `json:"Confidence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if result.RecognitionStatus != "Success" {
		return fmt.Errorf("azure recognition status: %s", result.RecognitionStatus)
	}

	if result.DisplayText != "" {
		event := TranscriptEvent{
			Text:       result.DisplayText,
			IsFinal:    true,
			Confidence: result.Confidence,
			Duration:   float64(result.Duration) / 10000000.0, // Convert 100ns units to seconds
		}

		select {
		case s.resultChan <- event:
		default:
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *azureStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000
	}
	bytesPerSecond := s.sampleRate * 2
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}
