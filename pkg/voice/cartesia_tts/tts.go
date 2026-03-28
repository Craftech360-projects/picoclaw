package cartesia_tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	defaultBaseURL    = "https://api.cartesia.ai"
	defaultAPIVersion = "2025-04-16"
	defaultModelID    = "sonic-3"
	defaultSampleRate = 24000
)

// CartesiaTTS streams audio from Cartesia text-to-speech.
type CartesiaTTS struct {
	cfg    TTSConfig
	client *http.Client
}

// NewCartesiaTTS creates a new Cartesia TTS client.
func NewCartesiaTTS(cfg TTSConfig) *CartesiaTTS {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.APIVersion) == "" {
		cfg.APIVersion = defaultAPIVersion
	}
	if strings.TrimSpace(cfg.ModelID) == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = defaultSampleRate
	}
	if strings.TrimSpace(cfg.Language) == "" {
		cfg.Language = "en"
	}
	return &CartesiaTTS{
		cfg: cfg,
		client: &http.Client{
			Timeout: 0,
		},
	}
}

// Synthesize starts streaming audio for the given text.
func (t *CartesiaTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("cartesia tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("cartesia api key is empty")
	}
	if strings.TrimSpace(t.cfg.VoiceID) == "" {
		return nil, errors.New("cartesia voice id is empty")
	}
	if strings.TrimSpace(t.cfg.ModelID) == "" {
		return nil, errors.New("cartesia model id is empty")
	}

	endpoint := strings.TrimRight(t.cfg.BaseURL, "/") + "/tts/bytes"
	payload := map[string]any{
		"model_id":   t.cfg.ModelID,
		"transcript": text,
		"voice": map[string]any{
			"mode": "id",
			"id":   t.cfg.VoiceID,
		},
		"output_format": map[string]any{
			"container":   "raw",
			"encoding":    "pcm_s16le",
			"sample_rate": t.cfg.SampleRateHz,
		},
		"language": t.cfg.Language,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cartesia-Version", t.cfg.APIVersion)
	setAuthHeaders(req, t.cfg.APIKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cartesia tts error: %s", string(data))
	}

	return &cartesiaAudioStream{body: resp.Body}, nil
}

type cartesiaAudioStream struct {
	body io.ReadCloser
}

func (s *cartesiaAudioStream) Read() ([]byte, error) {
	buf := make([]byte, 4096)
	n, err := s.body.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	if err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (s *cartesiaAudioStream) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func setAuthHeaders(req *http.Request, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	if strings.HasPrefix(strings.ToLower(apiKey), "bearer ") {
		req.Header.Set("Authorization", apiKey)
		return
	}
	req.Header.Set("X-API-Key", apiKey)
}
