package sarvam_tts

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
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultBaseURL = "wss://api.sarvam.ai"
	defaultModelID = "bulbul:v3"
	// defaultSampleRate matches the rest of the voice pipeline (Deepgram,
	// ElevenLabs, the device audio params) so the LiveKit PCM track and gateway
	// resampler line up. 22050 produced a track other stages didn't expect.
	defaultSampleRate = 24000
	fallbackLanguage  = "hi-IN"
	// outputAudioCodec "linear16" makes Sarvam stream raw 16-bit little-endian
	// PCM, which the voice pipeline consumes directly (no mp3/wav decoding).
	outputAudioCodec = "linear16"
	// defaultOutputBitrate only takes effect for compressed codecs (mp3);
	// ignored for linear16. Sent so a future codec switch keeps working.
	defaultOutputBitrate = "32k"
)

// defaultSpeaker is a valid bulbul:v3 speaker. (v2 speakers like "meera" and
// "anushka" are rejected by v3.) "sunny" chosen by ear for expressiveness.
const defaultSpeaker = "sunny"

// defaultTemperature is bulbul:v3's expressiveness knob (API default 0.6 is
// flat; 1.0 is the allowed max).
const defaultTemperature = 1.0

// supportedLangs are the ISO-639 language prefixes bulbul supports. English and
// Hindi both collapse to hi-IN (Hinglish via the Hindi voice) per product rule.
var supportedLangs = map[string]bool{
	"hi": true, "en": true, "bn": true, "gu": true, "kn": true,
	"ml": true, "mr": true, "od": true, "pa": true, "ta": true, "te": true,
}

// ResolveLanguageCode maps a session language code (e.g. "en-IN", "ta", "") to a
// Sarvam target_language_code. Empty/Hindi -> hi-IN; a supported
// language (including English -> en-IN) -> "<lang>-IN"; anything else -> hi-IN.
func ResolveLanguageCode(sessionCode string) string {
	code := strings.TrimSpace(sessionCode)
	if code == "" {
		return fallbackLanguage
	}
	lang := strings.ToLower(code)
	if idx := strings.IndexAny(lang, "-_"); idx >= 0 {
		lang = lang[:idx]
	}
	if lang == "hi" {
		return fallbackLanguage
	}
	if supportedLangs[lang] {
		return lang + "-IN"
	}
	return fallbackLanguage
}

// SarvamTTS streams audio from Sarvam's bulbul websocket text-to-speech API.
type SarvamTTS struct {
	cfg    TTSConfig
	dialer *websocket.Dialer
	// mu guards cfg.LanguageCode, which SetLanguage may update from the STT
	// goroutine while Synthesize reads it from the outbound goroutine.
	mu sync.RWMutex
}

// SetLanguage retargets synthesis to the language detected by STT (e.g.
// "ta-IN"). Empty/unknown/auto are ignored so VAD signal events don't reset a
// detected language back to the hi-IN fallback.
func (t *SarvamTTS) SetLanguage(detectedCode string) {
	code := strings.ToLower(strings.TrimSpace(detectedCode))
	if code == "" || code == "unknown" || code == "auto" {
		return
	}
	resolved := ResolveLanguageCode(detectedCode)
	t.mu.Lock()
	changed := t.cfg.LanguageCode != resolved
	t.cfg.LanguageCode = resolved
	t.mu.Unlock()
	if changed {
		logger.InfoCF("sarvam_tts", "TTS language switched to STT-detected language", map[string]any{
			"tts_provider":      "sarvam",
			"detected_code":     detectedCode,
			"tts_language_code": resolved,
		})
	}
}

// NewSarvamTTS creates a new Sarvam TTS client.
func NewSarvamTTS(cfg TTSConfig) *SarvamTTS {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.ModelID) == "" {
		cfg.ModelID = defaultModelID
	}
	if strings.TrimSpace(cfg.VoiceID) == "" {
		cfg.VoiceID = defaultSpeaker
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = defaultSampleRate
	}
	if strings.TrimSpace(cfg.LanguageCode) == "" {
		cfg.LanguageCode = fallbackLanguage
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = defaultTemperature
	}
	if strings.TrimSpace(cfg.OutputBitrate) == "" {
		cfg.OutputBitrate = defaultOutputBitrate
	}
	return &SarvamTTS{cfg: cfg, dialer: websocket.DefaultDialer}
}

// Synthesize opens a websocket, sends the config + text + flush, and streams PCM.
func (t *SarvamTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("sarvam tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("sarvam api key is empty")
	}

	t.mu.RLock()
	cfg := t.cfg
	t.mu.RUnlock()

	endpoint, err := buildWebSocketURL(cfg)
	if err != nil {
		return nil, err
	}

	logger.InfoCF("sarvam_tts", "Using Sarvam TTS provider", map[string]any{
		"tts_provider":       "sarvam",
		"tts_model_id":       cfg.ModelID,
		"tts_voice_id":       cfg.VoiceID,
		"tts_language_code":  cfg.LanguageCode,
		"tts_sample_rate_hz": cfg.SampleRateHz,
	})

	header := http.Header{}
	header.Set("Api-Subscription-Key", strings.TrimSpace(cfg.APIKey))
	conn, resp, err := t.dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		status := ""
		body := ""
		if resp != nil {
			status = resp.Status
			if resp.Body != nil {
				defer resp.Body.Close()
				data, _ := io.ReadAll(resp.Body)
				body = strings.TrimSpace(string(data))
			}
		}
		logger.ErrorCF("sarvam_tts", "Sarvam TTS websocket dial failed", map[string]any{
			"tts_provider": "sarvam",
			"tts_voice_id": cfg.VoiceID,
			"status":       status,
			"body":         body,
			"error":        err.Error(),
		})
		if body != "" || status != "" {
			return nil, fmt.Errorf("sarvam websocket dial: %w (status=%s body=%s)", err, status, body)
		}
		return nil, fmt.Errorf("sarvam websocket dial: %w", err)
	}

	configMsg := map[string]any{
		"type": "config",
		"data": map[string]any{
			"model":                cfg.ModelID,
			"target_language_code": cfg.LanguageCode,
			"speaker":              cfg.VoiceID,
			"speech_sample_rate":   strconv.Itoa(cfg.SampleRateHz),
			"output_audio_codec":   outputAudioCodec,
			"output_audio_bitrate": cfg.OutputBitrate,
			"temperature":          cfg.Temperature,
		},
	}
	textMsg := map[string]any{"type": "text", "data": map[string]any{"text": text}}
	flushMsg := map[string]any{"type": "flush"}

	for _, msg := range []map[string]any{configMsg, textMsg, flushMsg} {
		if err := conn.WriteJSON(msg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("sarvam websocket send %v: %w", msg["type"], err)
		}
	}

	return &sarvamAudioStream{conn: conn}, nil
}

type sarvamAudioStream struct {
	conn *websocket.Conn
}

type sarvamFrame struct {
	Type string `json:"type"`
	Data struct {
		Audio     string `json:"audio"`
		EventType string `json:"event_type"`
		Message   string `json:"message"`
	} `json:"data"`
}

func (s *sarvamAudioStream) Read() ([]byte, error) {
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

		var frame sarvamFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil, fmt.Errorf("decode sarvam tts message: %w", err)
		}

		switch frame.Type {
		case "audio":
			if strings.TrimSpace(frame.Data.Audio) == "" {
				continue
			}
			audio, err := base64.StdEncoding.DecodeString(frame.Data.Audio)
			if err != nil {
				return nil, fmt.Errorf("decode sarvam tts audio: %w", err)
			}
			if len(audio) == 0 {
				continue
			}
			return audio, nil
		case "event":
			if frame.Data.EventType == "final" {
				return nil, io.EOF
			}
			continue
		case "error":
			msg := strings.TrimSpace(frame.Data.Message)
			if msg == "" {
				msg = strings.TrimSpace(string(data))
			}
			// Common cause: voice_id is not a valid speaker for the model
			// (e.g. bulbul:v2 "meera" against bulbul:v3).
			logger.ErrorCF("sarvam_tts", "Sarvam TTS stream error", map[string]any{
				"tts_provider": "sarvam",
				"error":        msg,
			})
			return nil, fmt.Errorf("sarvam tts stream error: %s", msg)
		default:
			continue
		}
	}
}

func (s *sarvamAudioStream) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// buildWebSocketURL returns the Sarvam TTS streaming endpoint, coercing an
// http(s) base to ws(s) and appending the model + completion-event query.
func buildWebSocketURL(cfg TTSConfig) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	case strings.HasPrefix(base, "wss://"), strings.HasPrefix(base, "ws://"):
	default:
		return "", fmt.Errorf("unsupported sarvam base url scheme: %s", cfg.BaseURL)
	}

	parsed, err := url.Parse(base + "/text-to-speech/ws")
	if err != nil {
		return "", err
	}
	q := parsed.Query()
	q.Set("model", cfg.ModelID)
	q.Set("send_completion_event", "true")
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}
