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

const mistralTranscriptionURL = "https://api.mistral.ai/v1/audio/transcriptions"

// mistralProvider implements STT using Mistral Voxtral transcription API.
type mistralProvider struct {
	name   string
	apiKey string
	model  string
}

func NewMistralProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "voxtral-mini-latest"
	}
	return &mistralProvider{
		name:   "mistral",
		apiKey: apiKey,
		model:  model,
	}
}

// NewVoxtralProvider provides an alias name for the same Mistral Voxtral backend.
func NewVoxtralProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "voxtral-mini-latest"
	}
	return &mistralProvider{
		name:   "voxtral",
		apiKey: apiKey,
		model:  model,
	}
}

func (p *mistralProvider) Name() string { return p.name }

func (p *mistralProvider) WithConfig(apiKey, model string) Provider {
	if p.name == "voxtral" {
		return NewVoxtralProvider(apiKey, model)
	}
	return NewMistralProvider(apiKey, model)
}

func (p *mistralProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto", "en", "zh", "hi", "es", "ar", "fr", "pt", "ru", "de", "ja", "ko", "it", "nl"},
		Models:               []string{"voxtral-mini-latest", "voxtral-small-latest"},
		SupportsStreaming:    false,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *mistralProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("MISTRAL_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("mistral: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = "voxtral-mini-latest"
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	return &mistralStreamAdapter{
		apiKey:      apiKey,
		model:       model,
		language:    normalizeMistralLang(opts.Language),
		sampleRate:  sampleRate,
		audioBuffer: make([]byte, 0, sampleRate*2),
		resultChan:  make(chan TranscriptEvent, 10),
		ctx:         ctx,
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

type mistralStreamAdapter struct {
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

func (s *mistralStreamAdapter) SendAudio(pcm []byte) error {
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

func (s *mistralStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *mistralStreamAdapter) Finalize() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("stream is closed")
	}
	return s.transcribeLocked()
}

func (s *mistralStreamAdapter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	close(s.resultChan)
	return nil
}

func (s *mistralStreamAdapter) transcribeLocked() error {
	if len(s.audioBuffer) == 0 {
		return nil
	}

	wavData, err := createWAVFromPCM(s.audioBuffer, s.sampleRate)
	if err != nil {
		return fmt.Errorf("mistral: create WAV: %w", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return fmt.Errorf("mistral: create multipart file: %w", err)
	}
	if _, err := filePart.Write(wavData); err != nil {
		return fmt.Errorf("mistral: write multipart file: %w", err)
	}
	if err := writer.WriteField("model", s.model); err != nil {
		return fmt.Errorf("mistral: write model field: %w", err)
	}
	if s.language != "" {
		if err := writer.WriteField("language", s.language); err != nil {
			return fmt.Errorf("mistral: write language field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mistral: finalize multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, mistralTranscriptionURL, &body)
	if err != nil {
		return fmt.Errorf("mistral: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mistral: transcription request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mistral: transcription failed: %s - %s", resp.Status, string(respData))
	}

	var out struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(respData, &out); err != nil {
		return fmt.Errorf("mistral: decode response: %w", err)
	}

	if txt := strings.TrimSpace(out.Text); txt != "" {
		lang := strings.TrimSpace(out.Language)
		if lang == "" {
			lang = s.language
		}
		evt := TranscriptEvent{
			Text:      txt,
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

func (s *mistralStreamAdapter) calculateDuration() float64 {
	numSamples := len(s.audioBuffer) / 2
	return float64(numSamples) / float64(s.sampleRate)
}

func normalizeMistralLang(lang string) string {
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
	case "arabic":
		return "ar"
	case "french":
		return "fr"
	case "portuguese":
		return "pt"
	case "russian":
		return "ru"
	case "german":
		return "de"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "italian":
		return "it"
	case "dutch":
		return "nl"
	case "chinese", "mandarin":
		return "zh"
	default:
		if len(lang) == 2 {
			return lang
		}
		return ""
	}
}
