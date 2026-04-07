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

// assemblyaiProvider implements STT using AssemblyAI REST API (non-streaming with adapter)
type assemblyaiProvider struct {
	apiKey string
	model  string
}

// NewAssemblyAIProvider creates a new AssemblyAI provider
func NewAssemblyAIProvider(apiKey, model string) Provider {
	if model == "" {
		model = "universal"
	}
	return &assemblyaiProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *assemblyaiProvider) Name() string { return "assemblyai" }

func (p *assemblyaiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "auto"},
		Models:               []string{"universal", "universal_pro"},
		SupportsStreaming:    false, // Using REST API with adapter
		SupportsDiarization:  true,
		SupportsMultilingual: false,
	}
}

func (p *assemblyaiProvider) WithConfig(apiKey, model string) Provider {
	return NewAssemblyAIProvider(apiKey, model)
}

func (p *assemblyaiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("ASSEMBLYAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("assemblyai: API key not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	stream := &assemblyaiStreamAdapter{
		apiKey:      apiKey,
		model:       model,
		language:    opts.Language,
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

// assemblyaiStreamAdapter buffers audio and sends it to AssemblyAI REST API
type assemblyaiStreamAdapter struct {
	apiKey      string
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

func (s *assemblyaiStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}

	// Buffer the audio data
	s.audioBuffer = append(s.audioBuffer, pcm...)

	// If we have enough audio (based on endpointing), transcribe it
	if s.endpointing > 0 && len(s.audioBuffer) > s.endpointingThreshold() {
		return s.flushBuffer()
	}

	return nil
}

func (s *assemblyaiStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *assemblyaiStreamAdapter) Finalize() error {
	return s.flushBuffer()
}

func (s *assemblyaiStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

// flushBuffer sends buffered audio to AssemblyAI API and emits results
func (s *assemblyaiStreamAdapter) flushBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Create WAV file from PCM data
	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("create WAV: %w", err)
	}

	// Upload audio file to AssemblyAI
	uploadURL := "https://api.assemblyai.com/v2/upload"
	req, err := http.NewRequestWithContext(s.ctx, "POST", uploadURL, bytes.NewReader(wavData))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Authorization", s.apiKey)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload audio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %s - %s", resp.Status, string(body))
	}

	var uploadResp struct {
		UploadURL string `json:"upload_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return fmt.Errorf("decode upload response: %w", err)
	}

	// Create transcription request
	transcribeURL := "https://api.assemblyai.com/v2/transcript"
	transcriptReq := map[string]interface{}{
		"audio_url":     uploadResp.UploadURL,
		"speech_model":  s.model,
		"language_code": s.language,
	}

	if s.language == "" || s.language == "auto" {
		transcriptReq["language_detection"] = true
	}

	transcriptJSON, err := json.Marshal(transcriptReq)
	if err != nil {
		return fmt.Errorf("marshal transcript request: %w", err)
	}

	req, err = http.NewRequestWithContext(s.ctx, "POST", transcribeURL, bytes.NewReader(transcriptJSON))
	if err != nil {
		return fmt.Errorf("create transcript request: %w", err)
	}
	req.Header.Set("Authorization", s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err = s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("create transcription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transcription failed: %s - %s", resp.Status, string(body))
	}

	var transcriptResp struct {
		ID         string  `json:"id"`
		Status     string  `json:"status"`
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
		Language   string  `json:"language_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&transcriptResp); err != nil {
		return fmt.Errorf("decode transcript response: %w", err)
	}

	// Poll for completion if still processing
	for transcriptResp.Status == "processing" || transcriptResp.Status == "queued" {
		time.Sleep(500 * time.Millisecond)

		pollURL := fmt.Sprintf("https://api.assemblyai.com/v2/transcript/%s", transcriptResp.ID)
		req, err := http.NewRequestWithContext(s.ctx, "GET", pollURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", s.apiKey)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			continue
		}

		if err := json.NewDecoder(resp.Body).Decode(&transcriptResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
	}

	if transcriptResp.Status == "error" {
		return fmt.Errorf("transcription error")
	}

	// Send result
	if transcriptResp.Text != "" {
		event := TranscriptEvent{
			Text:       transcriptResp.Text,
			IsFinal:    true,
			Confidence: transcriptResp.Confidence,
			Language:   transcriptResp.Language,
			Duration:   s.calculateDuration(),
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

func (s *assemblyaiStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 30000 // ~1 second at 16kHz mono 16-bit
	}
	bytesPerSecond := s.sampleRate * 2 // 16-bit samples
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}

func (s *assemblyaiStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}
