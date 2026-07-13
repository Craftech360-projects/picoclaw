package smallest_tts

import (
	"context"
	"encoding/base64"
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
	defaultBaseURL    = "https://api.smallest.ai"
	defaultModelID    = "lightning_v3.1"
	defaultSampleRate = 24000
	// defaultVoiceID is a documented base-queue voice used only when no
	// voice is configured. In practice the real voice should come from
	// manager config (ttsConfig.VoiceID).
	defaultVoiceID = "liam"
)

// validSampleRates are the sample rates supported by the SmallestAI Waves API.
var validSampleRates = map[int]bool{
	8000:  true,
	16000: true,
	24000: true,
	44100: true,
}

// SmallestTTS streams audio from SmallestAI Waves text-to-speech.
type SmallestTTS struct {
	cfg    TTSConfig
	dialer *websocket.Dialer
}

// NewSmallestTTS creates a new SmallestAI TTS client.
func NewSmallestTTS(cfg TTSConfig) *SmallestTTS {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.ModelID) == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = parseSampleRate(cfg.OutputFormat)
	}
	return &SmallestTTS{
		cfg:    cfg,
		dialer: websocket.DefaultDialer,
	}
}

// Synthesize starts streaming audio for the given text.
func (t *SmallestTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("smallest tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("smallest api key is empty")
	}

	endpoint, err := buildWebSocketURL(t.cfg)
	if err != nil {
		return nil, err
	}

	logger.InfoCF("smallest_tts", "Using SmallestAI TTS provider", map[string]any{
		"tts_provider":       "smallest",
		"tts_model_id":       modelID(t.cfg),
		"tts_voice_id":       voiceID(t.cfg),
		"tts_output_format":  t.cfg.OutputFormat,
		"tts_sample_rate_hz": sampleRate(t.cfg),
	})

	header := http.Header{}
	header.Set("Authorization", "Bearer "+strings.TrimSpace(t.cfg.APIKey))
	conn, resp, err := t.dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("smallest websocket dial: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(data)))
		}
		return nil, fmt.Errorf("smallest websocket dial: %w", err)
	}

	request := map[string]any{
		"voice_id":    voiceID(t.cfg),
		"text":        text,
		"model":       modelID(t.cfg),
		"sample_rate": sampleRate(t.cfg),
		"flush":       true,
	}
	if err := conn.WriteJSON(request); err != nil {
		conn.Close()
		return nil, fmt.Errorf("smallest websocket send text: %w", err)
	}

	return &smallestAudioStream{conn: conn}, nil
}

type smallestAudioStream struct {
	conn *websocket.Conn
}

type smallestFrame struct {
	Status string `json:"status"`
	Data   struct {
		Audio string `json:"audio"`
	} `json:"data"`
	Message string `json:"message"`
}

func (s *smallestAudioStream) Read() ([]byte, error) {
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
		if messageType != websocket.TextMessage {
			continue
		}

		var frame smallestFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil, fmt.Errorf("decode smallest tts message: %w", err)
		}

		switch frame.Status {
		case "chunk":
			if strings.TrimSpace(frame.Data.Audio) == "" {
				continue
			}
			audio, err := base64.StdEncoding.DecodeString(frame.Data.Audio)
			if err != nil {
				return nil, fmt.Errorf("decode smallest tts audio: %w", err)
			}
			if len(audio) == 0 {
				continue
			}
			return audio, nil
		case "complete":
			return nil, io.EOF
		case "error":
			if strings.TrimSpace(frame.Message) != "" {
				return nil, fmt.Errorf("smallest tts stream error: %s", frame.Message)
			}
			return nil, fmt.Errorf("smallest tts stream error: %s", strings.TrimSpace(string(data)))
		default:
			// e.g. "word_timestamp" - not audio, keep reading.
			continue
		}
	}
}

func (s *smallestAudioStream) Close() error {
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
		return "", fmt.Errorf("unsupported smallest base url scheme: %s", cfg.BaseURL)
	}

	parsed, err := url.Parse(base + "/waves/v1/tts/live")
	if err != nil {
		return "", err
	}

	q := parsed.Query()
	q.Set("timeout", "120")
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func modelID(cfg TTSConfig) string {
	if model := strings.TrimSpace(cfg.ModelID); model != "" {
		return model
	}
	return defaultModelID
}

func voiceID(cfg TTSConfig) string {
	if voice := strings.TrimSpace(cfg.VoiceID); voice != "" {
		return voice
	}
	logger.WarnCF("smallest_tts", "No voice_id configured, falling back to default base-queue voice", map[string]any{
		"tts_provider":       "smallest",
		"tts_fallback_voice": defaultVoiceID,
	})
	return defaultVoiceID
}

func sampleRate(cfg TTSConfig) int {
	if cfg.SampleRateHz > 0 {
		return validSampleRate(cfg.SampleRateHz)
	}
	return validSampleRate(parseSampleRate(cfg.OutputFormat))
}

func validSampleRate(rate int) int {
	if validSampleRates[rate] {
		return rate
	}
	return defaultSampleRate
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
