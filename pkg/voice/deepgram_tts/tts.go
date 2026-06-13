package deepgram_tts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultBaseURL    = "https://api.deepgram.com"
	defaultModelID    = "aura-2-asteria-en"
	defaultEncoding   = "linear16"
	defaultSampleRate = 24000
)

// DeepgramTTS streams audio from Deepgram Aura text-to-speech.
type DeepgramTTS struct {
	cfg    TTSConfig
	dialer *websocket.Dialer
}

// NewDeepgramTTS creates a new Deepgram TTS client.
func NewDeepgramTTS(cfg TTSConfig) *DeepgramTTS {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.ModelID) == "" && strings.TrimSpace(cfg.VoiceID) == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = parseSampleRate(cfg.OutputFormat)
	}
	return &DeepgramTTS{
		cfg:    cfg,
		dialer: websocket.DefaultDialer,
	}
}

// Synthesize starts streaming audio for the given text.
func (t *DeepgramTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("deepgram tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("deepgram api key is empty")
	}

	endpoint, err := buildWebSocketURL(t.cfg)
	if err != nil {
		return nil, err
	}

	logger.InfoCF("deepgram_tts", "Using Deepgram TTS provider", map[string]any{
		"tts_provider":       "deepgram",
		"tts_model_id":       modelID(t.cfg),
		"tts_output_format":  t.cfg.OutputFormat,
		"tts_sample_rate_hz": sampleRate(t.cfg),
	})

	header := http.Header{}
	header.Set("Authorization", formatAuthHeader(t.cfg.APIKey))
	conn, resp, err := t.dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("deepgram websocket dial: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(data)))
		}
		return nil, fmt.Errorf("deepgram websocket dial: %w", err)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "Speak",
		"text": text,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("deepgram websocket send text: %w", err)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "Close",
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("deepgram websocket close input: %w", err)
	}

	return &deepgramAudioStream{conn: conn}, nil
}

type deepgramAudioStream struct {
	conn *websocket.Conn
}

type deepgramControlMessage struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Error       string `json:"error"`
}

func (s *deepgramAudioStream) Read() ([]byte, error) {
	if s.conn == nil {
		return nil, io.EOF
	}

	for {
		messageType, data, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil, io.EOF
			}
			return nil, err
		}

		if messageType == websocket.BinaryMessage {
			if len(data) == 0 {
				continue
			}
			return data, nil
		}
		if messageType != websocket.TextMessage {
			continue
		}

		var msg deepgramControlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("decode deepgram tts message: %w", err)
		}
		if isErrorMessage(msg) {
			return nil, fmt.Errorf("deepgram tts stream error: %s", formatControlError(msg))
		}
	}
}

func (s *deepgramAudioStream) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func buildWebSocketURL(cfg TTSConfig) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	case strings.HasPrefix(base, "wss://"), strings.HasPrefix(base, "ws://"):
	default:
		return "", fmt.Errorf("unsupported deepgram base url scheme: %s", cfg.BaseURL)
	}

	parsed, err := url.Parse(base + "/v1/speak")
	if err != nil {
		return "", err
	}

	q := parsed.Query()
	q.Set("encoding", outputEncoding(cfg.OutputFormat))
	q.Set("model", modelID(cfg))
	q.Set("sample_rate", strconv.Itoa(sampleRate(cfg)))
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func formatAuthHeader(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	lower := strings.ToLower(apiKey)
	if strings.HasPrefix(lower, "token ") || strings.HasPrefix(lower, "bearer ") {
		return apiKey
	}
	return "Token " + apiKey
}

func modelID(cfg TTSConfig) string {
	if model := strings.TrimSpace(cfg.ModelID); model != "" {
		return model
	}
	if voice := strings.TrimSpace(cfg.VoiceID); voice != "" {
		return voice
	}
	return defaultModelID
}

func outputEncoding(outputFormat string) string {
	format := strings.ToLower(strings.TrimSpace(outputFormat))
	switch {
	case strings.HasPrefix(format, "mulaw"):
		return "mulaw"
	case strings.HasPrefix(format, "alaw"):
		return "alaw"
	default:
		return defaultEncoding
	}
}

func sampleRate(cfg TTSConfig) int {
	if cfg.SampleRateHz > 0 {
		return cfg.SampleRateHz
	}
	return parseSampleRate(cfg.OutputFormat)
}

func parseSampleRate(outputFormat string) int {
	format := strings.ToLower(strings.TrimSpace(outputFormat))
	for _, part := range strings.FieldsFunc(format, func(r rune) bool {
		return r == '_' || r == '-' || r == ':'
	}) {
		value, err := strconv.Atoi(part)
		if err == nil && value > 0 {
			return value
		}
	}
	return defaultSampleRate
}

func isErrorMessage(msg deepgramControlMessage) bool {
	return strings.EqualFold(msg.Type, "Error") ||
		strings.TrimSpace(msg.Error) != "" ||
		strings.HasPrefix(strings.ToUpper(strings.TrimSpace(msg.Code)), "ERROR")
}

func formatControlError(msg deepgramControlMessage) string {
	if text := strings.TrimSpace(msg.Message); text != "" {
		return text
	}
	if text := strings.TrimSpace(msg.Description); text != "" {
		return text
	}
	if text := strings.TrimSpace(msg.Error); text != "" {
		return text
	}
	if text := strings.TrimSpace(msg.Code); text != "" {
		return text
	}
	return msg.Type
}
