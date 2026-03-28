package elevenlabs_tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultBaseURL = "https://api.elevenlabs.io"

// ElevenLabsTTS streams audio from ElevenLabs text-to-speech.
type ElevenLabsTTS struct {
	cfg    TTSConfig
	client *http.Client
}

// NewElevenLabsTTS creates a new ElevenLabs TTS client.
func NewElevenLabsTTS(cfg TTSConfig) *ElevenLabsTTS {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &ElevenLabsTTS{
		cfg: cfg,
		client: &http.Client{
			Timeout: 0,
		},
	}
}

// Synthesize starts streaming audio for the given text.
func (t *ElevenLabsTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("elevenlabs tts is nil")
	}
	if t.cfg.APIKey == "" {
		return nil, errors.New("elevenlabs api key is empty")
	}
	if t.cfg.VoiceID == "" {
		return nil, errors.New("elevenlabs voice id is empty")
	}

	endpoint, err := buildStreamURL(t.cfg)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"text": text,
	}
	if strings.TrimSpace(t.cfg.ModelID) != "" {
		payload["model_id"] = t.cfg.ModelID
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
	req.Header.Set("xi-api-key", t.cfg.APIKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("elevenlabs tts error: %s", string(data))
	}

	return &elevenLabsAudioStream{body: resp.Body}, nil
}

type elevenLabsAudioStream struct {
	body io.ReadCloser
}

func (s *elevenLabsAudioStream) Read() ([]byte, error) {
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

func (s *elevenLabsAudioStream) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func buildStreamURL(cfg TTSConfig) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	path := fmt.Sprintf("%s/v1/text-to-speech/%s/stream", base, cfg.VoiceID)
	parsed, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.OutputFormat) != "" {
		q := parsed.Query()
		q.Set("output_format", cfg.OutputFormat)
		parsed.RawQuery = q.Encode()
	}
	return parsed.String(), nil
}

var _ TTSProvider = (*ElevenLabsTTS)(nil)
