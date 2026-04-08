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
	"strings"
	"sync"
	"time"
)

const sarvamSTTURL = "https://api.sarvam.ai/speech-to-text"

// sarvamProvider implements STT using Sarvam REST API.
type sarvamProvider struct {
	apiKey string
	model  string
}

func NewSarvamProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "saaras:v3"
	}
	return &sarvamProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *sarvamProvider) Name() string { return "sarvam" }

func (p *sarvamProvider) WithConfig(apiKey, model string) Provider {
	return NewSarvamProvider(apiKey, model)
}

func (p *sarvamProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{
			"auto", "unknown",
			"hi-IN", "bn-IN", "gu-IN", "kn-IN", "ml-IN", "mr-IN", "od-IN", "pa-IN", "ta-IN", "te-IN", "en-IN",
			"as-IN", "ur-IN", "ne-IN", "kok-IN", "ks-IN", "sd-IN", "sa-IN", "sat-IN", "mni-IN", "brx-IN", "mai-IN", "doi-IN",
		},
		Models:               []string{"saaras:v3", "saarika:v2.5"},
		SupportsStreaming:    false,
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *sarvamProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("SARVAM_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("sarvam: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = "saaras:v3"
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	return &sarvamStreamAdapter{
		apiKey:      apiKey,
		model:       model,
		language:    normalizeSarvamLang(opts.Language),
		sampleRate:  sampleRate,
		audioBuffer: make([]byte, 0, sampleRate*2),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

type sarvamStreamAdapter struct {
	apiKey      string
	model       string
	language    string
	sampleRate  int
	audioBuffer []byte
	resultChan  chan TranscriptEvent
	ctx         context.Context
	httpClient  *http.Client
	mu          sync.Mutex
	closed      bool
}

func (s *sarvamStreamAdapter) SendAudio(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}
	if len(pcm) == 0 {
		return nil
	}
	s.audioBuffer = append(s.audioBuffer, pcm...)
	return nil
}

func (s *sarvamStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *sarvamStreamAdapter) Finalize() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}
	return s.transcribeLocked()
}

func (s *sarvamStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *sarvamStreamAdapter) transcribeLocked() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("sarvam: create WAV: %w", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return fmt.Errorf("sarvam: create multipart file: %w", err)
	}
	if _, err := filePart.Write(wavData); err != nil {
		return fmt.Errorf("sarvam: write multipart file: %w", err)
	}
	if err := writer.WriteField("model", s.model); err != nil {
		return fmt.Errorf("sarvam: write model field: %w", err)
	}
	if s.language != "" {
		if err := writer.WriteField("language_code", s.language); err != nil {
			return fmt.Errorf("sarvam: write language field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("sarvam: finalize multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, sarvamSTTURL, &body)
	if err != nil {
		return fmt.Errorf("sarvam: create request: %w", err)
	}
	req.Header.Set("api-subscription-key", s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sarvam: transcription request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sarvam: transcription failed: %s - %s", resp.Status, string(respData))
	}

	var out struct {
		Transcript   string `json:"transcript"`
		LanguageCode string `json:"language_code"`
	}
	if err := json.Unmarshal(respData, &out); err != nil {
		return fmt.Errorf("sarvam: decode response: %w", err)
	}

	text := strings.TrimSpace(out.Transcript)
	if text != "" {
		lang := strings.TrimSpace(out.LanguageCode)
		if lang == "" {
			lang = s.language
		}
		evt := TranscriptEvent{
			Text:      text,
			IsFinal:   true,
			SpeechEnd: true,
			Language:  lang,
			Duration:  s.calculateDuration(),
		}
		select {
		case s.resultChan <- evt:
		default:
		}
	}

	s.audioBuffer = s.audioBuffer[:0]
	return nil
}

func (s *sarvamStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}

func normalizeSarvamLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "", "auto", "unknown":
		return "unknown"
	case "english", "en":
		return "en-IN"
	case "hindi", "hi":
		return "hi-IN"
	case "bengali", "bn":
		return "bn-IN"
	case "gujarati", "gu":
		return "gu-IN"
	case "kannada", "kn":
		return "kn-IN"
	case "malayalam", "ml":
		return "ml-IN"
	case "marathi", "mr":
		return "mr-IN"
	case "odia", "or", "od":
		return "od-IN"
	case "punjabi", "pa":
		return "pa-IN"
	case "tamil", "ta":
		return "ta-IN"
	case "telugu", "te":
		return "te-IN"
	default:
		// Pass through valid BCP-47 style values (e.g. hi-IN, en-IN, ur-IN).
		if strings.Contains(lang, "-") {
			return strings.TrimSpace(lang)
		}
		return "unknown"
	}
}
