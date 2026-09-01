package smallest_tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

const (
	defaultBaseURL    = "https://waves-api.smallest.ai"
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

// SmallestTTS synthesizes audio via the SmallestAI Waves batch endpoint.
//
// The streaming `/waves/v1/tts/live` WebSocket was measured to deliver every
// audio chunk within ~640ms and then hold the socket open for a further ~4s
// before sending its `complete` frame — a per-sentence stall the listener hears
// as a gap. No client-side end-signal shortens it (a plain flush, a separate
// flush message, and `continue:false` all waited ~4s; closing early truncates
// the audio). The batch endpoint returns the same PCM in ~880ms, so we use it
// and adapt the buffer to the streaming interface.
type SmallestTTS struct {
	cfg    TTSConfig
	client *http.Client
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
		client: &http.Client{},
	}
}

// Synthesize returns the full PCM for text as a single-buffer stream.
func (t *SmallestTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("smallest tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("smallest api key is empty")
	}

	endpoint, err := buildSpeechURL(t.cfg)
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

	payload, err := json.Marshal(map[string]any{
		"text":          text,
		"voice_id":      voiceID(t.cfg),
		"sample_rate":   sampleRate(t.cfg),
		"output_format": "pcm",
		"speed":         1,
	})
	if err != nil {
		return nil, fmt.Errorf("smallest tts encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("smallest tts build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(t.cfg.APIKey))
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("smallest tts request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("smallest tts read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("smallest tts status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if len(body) == 0 {
		return nil, errors.New("smallest tts returned no audio")
	}

	return tts.NewBufferStream(body), nil
}

// buildSpeechURL returns {base}/api/v1/{model}/get_speech. The REST path spells
// the model with hyphens (lightning-v3.1) while the request body and our config
// use underscores (lightning_v3.1).
func buildSpeechURL(cfg TTSConfig) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	switch {
	case strings.HasPrefix(base, "https://"), strings.HasPrefix(base, "http://"):
	case strings.HasPrefix(base, "wss://"):
		base = "https://" + strings.TrimPrefix(base, "wss://")
	case strings.HasPrefix(base, "ws://"):
		base = "http://" + strings.TrimPrefix(base, "ws://")
	default:
		return "", fmt.Errorf("unsupported smallest base url scheme: %s", cfg.BaseURL)
	}

	pathModel := strings.ReplaceAll(modelID(cfg), "_", "-")
	parsed, err := url.Parse(base + "/api/v1/" + pathModel + "/get_speech")
	if err != nil {
		return "", err
	}
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
