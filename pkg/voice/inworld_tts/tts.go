package inworld_tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultBaseURL = "https://api.inworld.ai"

// InworldTTS streams audio from Inworld text-to-speech.
type InworldTTS struct {
	cfg    TTSConfig
	client *http.Client
}

// NewInworldTTS creates a new Inworld TTS client.
func NewInworldTTS(cfg TTSConfig) *InworldTTS {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = 22050
	}
	return &InworldTTS{
		cfg: cfg,
		client: &http.Client{
			Timeout: 0,
		},
	}
}

// Synthesize starts streaming audio for the given text.
func (t *InworldTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("inworld tts is nil")
	}
	if t.cfg.APIKey == "" {
		return nil, errors.New("inworld api key is empty")
	}
	if t.cfg.VoiceID == "" {
		return nil, errors.New("inworld voice id is empty")
	}
	if t.cfg.ModelID == "" {
		return nil, errors.New("inworld model id is empty")
	}

	endpoint := strings.TrimRight(t.cfg.BaseURL, "/") + "/tts/v1/voice:stream"
	payload := map[string]any{
		"text":    text,
		"voiceId": t.cfg.VoiceID,
		"modelId": t.cfg.ModelID,
		"audioConfig": map[string]any{
			"audioEncoding":   "LINEAR16",
			"sampleRateHertz": t.cfg.SampleRateHz,
		},
	}
	if t.cfg.Temperature > 0 {
		payload["temperature"] = t.cfg.Temperature
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
	req.Header.Set("Authorization", formatAuthHeader(t.cfg.APIKey))

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("inworld tts error: %s", string(data))
	}

	return &inworldAudioStream{
		body:    resp.Body,
		decoder: json.NewDecoder(resp.Body),
	}, nil
}

type inworldAudioStream struct {
	body    io.ReadCloser
	decoder *json.Decoder
}

type streamChunk struct {
	Result *struct {
		AudioContent string `json:"audioContent"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *inworldAudioStream) Read() ([]byte, error) {
	if s.decoder == nil {
		return nil, io.EOF
	}

	for {
		var chunk streamChunk
		if err := s.decoder.Decode(&chunk); err != nil {
			return nil, err
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("inworld tts stream error: %s", chunk.Error.Message)
		}
		if chunk.Result == nil || chunk.Result.AudioContent == "" {
			continue
		}

		audioBytes, err := base64.StdEncoding.DecodeString(chunk.Result.AudioContent)
		if err != nil {
			return nil, fmt.Errorf("decode inworld audio: %w", err)
		}
		audioBytes = stripWAVHeader(audioBytes)
		if len(audioBytes) == 0 {
			continue
		}
		return audioBytes, nil
	}
}

func (s *inworldAudioStream) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func formatAuthHeader(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(apiKey), "basic ") {
		return apiKey
	}
	return "Basic " + apiKey
}

func stripWAVHeader(audio []byte) []byte {
	if len(audio) < 12 {
		return audio
	}
	if !bytes.Equal(audio[0:4], []byte("RIFF")) || !bytes.Equal(audio[8:12], []byte("WAVE")) {
		return audio
	}

	offset := 12
	for offset+8 <= len(audio) {
		chunkID := audio[offset : offset+4]
		chunkSize := int(uint32(audio[offset+4]) | uint32(audio[offset+5])<<8 | uint32(audio[offset+6])<<16 | uint32(audio[offset+7])<<24)
		offset += 8
		if chunkSize < 0 || offset+chunkSize > len(audio) {
			return audio
		}
		if bytes.Equal(chunkID, []byte("data")) {
			return audio[offset : offset+chunkSize]
		}
		offset += chunkSize
		if chunkSize%2 == 1 {
			offset++
		}
	}

	return audio
}
