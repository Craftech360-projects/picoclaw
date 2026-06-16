package openai_tts

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
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModelID = "tts-1"
	// OpenAI's "pcm" response_format is 24kHz, signed 16-bit, mono. speaches follows suit.
	defaultSampleRate = 24000
)

// OpenAITTS streams audio from an OpenAI-compatible /audio/speech endpoint.
type OpenAITTS struct {
	cfg    TTSConfig
	client *http.Client
}

// NewOpenAITTS creates a new OpenAI-compatible TTS client.
func NewOpenAITTS(cfg TTSConfig) *OpenAITTS {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.ModelID) == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = defaultSampleRate
	}
	return &OpenAITTS{
		cfg:    cfg,
		client: &http.Client{Timeout: 0},
	}
}

// Synthesize POSTs to {base}/audio/speech and streams back raw PCM.
func (t *OpenAITTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("openai tts is nil")
	}
	if strings.TrimSpace(t.cfg.VoiceID) == "" {
		return nil, errors.New("openai tts voice id is empty")
	}

	endpoint := strings.TrimRight(t.cfg.BaseURL, "/") + "/audio/speech"
	payload := map[string]any{
		"model":           t.cfg.ModelID,
		"input":           text,
		"voice":           t.cfg.VoiceID,
		"response_format": "pcm",
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
	if key := strings.TrimSpace(t.cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai tts error: %s", string(data))
	}

	return &openaiAudioStream{body: resp.Body}, nil
}

type openaiAudioStream struct {
	body io.ReadCloser
}

func (s *openaiAudioStream) Read() ([]byte, error) {
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

func (s *openaiAudioStream) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
