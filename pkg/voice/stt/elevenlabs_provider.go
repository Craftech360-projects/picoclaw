package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"sync"
	"time"
)

// elevenlabsProvider implements STT using ElevenLabs Scribe v2 API
type elevenlabsProvider struct {
	apiKey string
	model  string
}

// NewElevenLabsProvider creates a new ElevenLabs provider
func NewElevenLabsProvider(apiKey, model string) Provider {
	if model == "" {
		model = "scribe_v2"
	}
	return &elevenlabsProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *elevenlabsProvider) Name() string { return "elevenlabs" }

func (p *elevenlabsProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"}, // Scribe v2 supports 190 languages
		Models:               []string{"scribe_v2"},
		SupportsStreaming:    false,
		SupportsDiarization:  false,
		SupportsMultilingual: true, // 190 languages
	}
}

func (p *elevenlabsProvider) WithConfig(apiKey, model string) Provider {
	return NewElevenLabsProvider(apiKey, model)
}

func (p *elevenlabsProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("ELEVENLABS_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("elevenlabs: API key not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	stream := &elevenlabsStreamAdapter{
		apiKey:      apiKey,
		model:       model,
		sampleRate:  opts.SampleRate,
		endpointing: time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer: make([]byte, 0),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		mu:          sync.Mutex{},
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}

	return stream, nil
}

// elevenlabsStreamAdapter handles ElevenLabs transcription
type elevenlabsStreamAdapter struct {
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

func (s *elevenlabsStreamAdapter) SendAudio(pcm []byte) error {
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

func (s *elevenlabsStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *elevenlabsStreamAdapter) Finalize() error {
	return s.transcribeBuffer()
}

func (s *elevenlabsStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *elevenlabsStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create MP3 from PCM (ElevenLabs accepts multiple formats)
	mp3Data, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create audio: %w", err)
	}

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(mp3Data); err != nil {
		return fmt.Errorf("write audio data: %w", err)
	}

	// Add model field
	if err := writer.WriteField("model_id", s.model); err != nil {
		return fmt.Errorf("write model: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}

	// Create request
	url := "https://api.elevenlabs.io/v1/speech-to-text"
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("xi-api-key", s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transcription failed: %s - %s", resp.Status, string(responseBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if result.Text != "" {
		event := TranscriptEvent{
			Text:    result.Text,
			IsFinal: true,
		}

		select {
		case s.resultChan <- event:
		default:
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *elevenlabsStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000
	}
	bytesPerSecond := s.sampleRate * 2
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}
