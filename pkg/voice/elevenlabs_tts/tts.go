package elevenlabs_tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

const defaultBaseURL = "https://api.elevenlabs.io"

// Voice settings tuned for expressive kids' character speech. Lower stability
// widens emotional range; below ~0.25 delivery starts to wobble and hallucinate.
// ponytail: env-tunable so voice can be calibrated without a redeploy.
const (
	defaultStability       = 0.35
	defaultSimilarityBoost = 0.75
	defaultStyle           = 0.45
)

// envFloat reads a [0,1] float from env, falling back to def when unset or invalid.
func envFloat(key string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64)
	if err != nil || v < 0 || v > 1 {
		return def
	}
	return v
}

// emotionShift maps a Cheeko Face emotion tag to stability/style deltas applied
// on top of the configured base, plus an absolute speed. Stability and style
// alone barely separate lively from soft; speed is what makes the two buckets
// audibly distinct, same as pace does in sarvam_tts emotionProsody.
// ponytail: coarse buckets; per-emotion tuning if kids notice.
func emotionShift(emotion string) (dStability, dStyle, speed float64) {
	switch emotion {
	case "happy", "excited", "laughing", "surprised", "silly":
		return -0.20, +0.30, 1.12
	case "sad", "sleepy", "crying", "scared", "shy":
		return +0.35, -0.30, 0.85
	}
	return 0, 0, 1.0
}

func clamp01(v float64) float64 {
	return math.Min(1, math.Max(0, v))
}

// voiceSettings builds the ElevenLabs voice_settings payload, shaded by the
// per-sentence emotion tag the pipeline stashed in ctx.
func voiceSettings(ctx context.Context) map[string]any {
	dStability, dStyle, speed := emotionShift(tts.EmotionFromContext(ctx))
	return map[string]any{
		"stability":         clamp01(envFloat("ELEVENLABS_STABILITY", defaultStability) + dStability),
		"similarity_boost":  envFloat("ELEVENLABS_SIMILARITY_BOOST", defaultSimilarityBoost),
		"style":             clamp01(envFloat("ELEVENLABS_STYLE", defaultStyle) + dStyle),
		"speed":             speed,
		"use_speaker_boost": true,
	}
}

// ElevenLabsTTS streams audio from ElevenLabs text-to-speech.
type ElevenLabsTTS struct {
	cfg    TTSConfig
	dialer *websocket.Dialer
}

// NewElevenLabsTTS creates a new ElevenLabs TTS client.
func NewElevenLabsTTS(cfg TTSConfig) *ElevenLabsTTS {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &ElevenLabsTTS{
		cfg:    cfg,
		dialer: websocket.DefaultDialer,
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

	endpoint, err := buildWebSocketURL(t.cfg)
	if err != nil {
		return nil, err
	}

	conn, resp, err := t.dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			data, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("elevenlabs websocket dial: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(data)))
		}
		return nil, fmt.Errorf("elevenlabs websocket dial: %w", err)
	}

	if err := conn.WriteJSON(map[string]any{
		"text":           " ",
		"xi_api_key":     t.cfg.APIKey,
		"voice_settings": voiceSettings(ctx),
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("elevenlabs websocket initialize: %w", err)
	}
	if err := conn.WriteJSON(map[string]any{
		"text":  text,
		"flush": true,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("elevenlabs websocket send text: %w", err)
	}
	if err := conn.WriteJSON(map[string]any{
		"text": "",
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("elevenlabs websocket close input: %w", err)
	}

	return &elevenLabsAudioStream{conn: conn}, nil
}

type elevenLabsAudioStream struct {
	conn          *websocket.Conn
	eofAfterAudio bool
}

type elevenLabsWebSocketMessage struct {
	Audio   string          `json:"audio"`
	IsFinal bool            `json:"isFinal"`
	Error   json.RawMessage `json:"error"`
}

func (s *elevenLabsAudioStream) Read() ([]byte, error) {
	if s.eofAfterAudio {
		return nil, io.EOF
	}
	if s.conn == nil {
		return nil, io.EOF
	}

	for {
		var msg elevenLabsWebSocketMessage
		if err := s.conn.ReadJSON(&msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil, io.EOF
			}
			return nil, err
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("elevenlabs tts stream error: %s", formatWebSocketError(msg.Error))
		}
		if msg.Audio != "" {
			audioBytes, err := base64.StdEncoding.DecodeString(msg.Audio)
			if err != nil {
				return nil, fmt.Errorf("decode elevenlabs audio: %w", err)
			}
			if msg.IsFinal {
				s.eofAfterAudio = true
			}
			if len(audioBytes) > 0 {
				return audioBytes, nil
			}
		}
		if msg.IsFinal {
			return nil, io.EOF
		}
	}
}

func (s *elevenLabsAudioStream) Close() error {
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
		return "", fmt.Errorf("unsupported elevenlabs base url scheme: %s", cfg.BaseURL)
	}

	path := fmt.Sprintf("%s/v1/text-to-speech/%s/stream-input", base, url.PathEscape(cfg.VoiceID))
	parsed, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.ModelID) != "" {
		q := parsed.Query()
		q.Set("model_id", cfg.ModelID)
		parsed.RawQuery = q.Encode()
	}
	if strings.TrimSpace(cfg.OutputFormat) != "" {
		q := parsed.Query()
		q.Set("output_format", cfg.OutputFormat)
		parsed.RawQuery = q.Encode()
	}
	return parsed.String(), nil
}

func formatWebSocketError(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
		return text
	}

	var obj struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if strings.TrimSpace(obj.Message) != "" {
			return obj.Message
		}
		if strings.TrimSpace(obj.Error) != "" {
			return obj.Error
		}
	}

	return string(raw)
}
