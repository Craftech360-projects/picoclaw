package stt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// googleProvider implements STT using Google Cloud Speech-to-Text API
type googleProvider struct {
	apiKey      string
	model       string
	language    string
	enableDiarization bool
}

// NewGoogleProvider creates a new Google Cloud Speech provider
func NewGoogleProvider(apiKey, model, language string, enableDiarization bool) Provider {
	if model == "" {
		model = "latest_long"
	}
	return &googleProvider{
		apiKey:            apiKey,
		model:             model,
		language:          language,
		enableDiarization: enableDiarization,
	}
}

func (p *googleProvider) Name() string { return "google" }

func (p *googleProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "hi", "it", "ja", "ko", "pt", "ru", "zh", "auto"},
		Models:               []string{"latest_long", "latest_short", "phone_call", "video"},
		SupportsStreaming:    false,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *googleProvider) WithConfig(apiKey, model string) Provider {
	return NewGoogleProvider(apiKey, model, "", false)
}

func (p *googleProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_CLOUD_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("google cloud: API key not configured")
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}

	language := opts.Language
	if language == "" && p.language != "" {
		language = p.language
	}

	stream := &googleStreamAdapter{
		apiKey:            apiKey,
		model:             model,
		language:          language,
		enableDiarization: p.enableDiarization || opts.Channels > 1,
		sampleRate:        opts.SampleRate,
		endpointing:       time.Duration(opts.EndpointingMS) * time.Millisecond,
		audioBuffer:       make([]byte, 0),
		resultChan:        make(chan TranscriptEvent, 10),
		ctx:               ctx,
		mu:                sync.Mutex{},
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	return stream, nil
}

// googleStreamAdapter handles Google Cloud Speech transcription
type googleStreamAdapter struct {
	apiKey            string
	model             string
	language          string
	enableDiarization bool
	sampleRate        int
	endpointing       time.Duration
	audioBuffer       []byte
	resultChan        chan TranscriptEvent
	ctx               context.Context
	mu                sync.Mutex
	closed            bool
	httpClient        *http.Client
}

func (s *googleStreamAdapter) SendAudio(pcm []byte) error {
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

func (s *googleStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *googleStreamAdapter) Finalize() error {
	return s.transcribeBuffer()
}

func (s *googleStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *googleStreamAdapter) transcribeBuffer() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	// Encode audio to base64 (required by Google Cloud API)
	audioBase64 := base64.StdEncoding.EncodeToString(s.audioBuffer)

	// Build request
	requestBody := map[string]interface{}{
		"config": map[string]interface{}{
			"encoding":        "LINEAR16",
			"sampleRateHertz": s.sampleRate,
			"audioChannelCount": 1,
			"languageCode":    s.language,
			"model":           s.model,
			"enableAutomaticPunctuation": true,
		},
		"audio": map[string]string{
			"content": audioBase64,
		},
	}

	// Add diarization config if enabled
	if s.enableDiarization {
		requestBody["config"].(map[string]interface{})["enableSpeakerDiarization"] = true
		requestBody["config"].(map[string]interface{})["diarizationSpeakerCount"] = 2
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Call Google Cloud Speech API
	url := fmt.Sprintf("https://speech.googleapis.com/v1/speech:recognize?key=%s", s.apiKey)
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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
		Results []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
			LanguageCode string `json:"languageCode"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	// Extract best alternative
	if len(result.Results) > 0 && len(result.Results[0].Alternatives) > 0 {
		alt := result.Results[0].Alternatives[0]
		event := TranscriptEvent{
			Text:       alt.Transcript,
			IsFinal:    true,
			Confidence: alt.Confidence,
			Language:   result.Results[0].LanguageCode,
			Duration:   s.calculateDuration(),
		}

		select {
		case s.resultChan <- event:
		default:
		}
	}

	s.audioBuffer = make([]byte, 0)
	return nil
}

func (s *googleStreamAdapter) endpointingThreshold() int {
	if s.endpointing <= 0 {
		return 32000
	}
	bytesPerSecond := s.sampleRate * 2
	return int(s.endpointing.Seconds() * float64(bytesPerSecond))
}

func (s *googleStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}
