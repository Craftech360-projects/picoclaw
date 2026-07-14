package azure_tts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

const (
	defaultVoiceID    = "en-US-AnaNeural"
	defaultSampleRate = 24000
)

var ssmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// AzureTTS synthesizes audio from the Azure Speech REST API.
type AzureTTS struct {
	cfg    TTSConfig
	client *http.Client
}

// NewAzureTTS creates a new Azure TTS client.
func NewAzureTTS(cfg TTSConfig) *AzureTTS {
	if strings.TrimSpace(cfg.VoiceID) == "" {
		cfg.VoiceID = defaultVoiceID
	}
	if cfg.SampleRateHz == 0 {
		cfg.SampleRateHz = defaultSampleRate
	}
	return &AzureTTS{cfg: cfg, client: &http.Client{}}
}

// Synthesize performs one batch request and returns the PCM as a stream.
func (t *AzureTTS) Synthesize(ctx context.Context, text string) (AudioStream, error) {
	if t == nil {
		return nil, errors.New("azure tts is nil")
	}
	if strings.TrimSpace(t.cfg.APIKey) == "" {
		return nil, errors.New("azure api key is empty")
	}
	if strings.TrimSpace(t.cfg.Endpoint) == "" {
		return nil, errors.New("azure endpoint is empty (set AZURE_SPEECH_REGION or AZURE_SPEECH_ENDPOINT)")
	}

	outputFormat, sampleRate := azureOutputFormat(t.cfg.SampleRateHz)

	logger.InfoCF("azure_tts", "Using Azure TTS provider", map[string]any{
		"tts_provider":       "azure",
		"tts_voice_id":       t.cfg.VoiceID,
		"tts_output_format":  outputFormat,
		"tts_sample_rate_hz": sampleRate,
	})

	ssml := buildSSML(t.cfg.VoiceID, text)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.Endpoint, strings.NewReader(ssml))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", strings.TrimSpace(t.cfg.APIKey))
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", outputFormat)
	req.Header.Set("User-Agent", "picoclaw-livekit")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure tts request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azure tts status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return tts.NewBufferStream(body), nil
}

// azureOutputFormat maps a sample rate to the Azure raw-PCM format string and
// the sample rate actually delivered (defaults to 24 kHz for unsupported rates).
func azureOutputFormat(sampleRateHz int) (string, int) {
	switch sampleRateHz {
	case 8000:
		return "raw-8khz-16bit-mono-pcm", 8000
	case 16000:
		return "raw-16khz-16bit-mono-pcm", 16000
	case 48000:
		return "raw-48khz-16bit-mono-pcm", 48000
	default:
		return "raw-24khz-16bit-mono-pcm", 24000
	}
}

// buildSSML wraps text in a minimal SSML document. xml:lang is derived from the
// voice name (e.g. "en-US-AnaNeural" -> "en-US").
func buildSSML(voice, text string) string {
	lang := ssmlLangFromVoice(voice)
	return fmt.Sprintf(
		`<speak version='1.0' xml:lang='%s'><voice name='%s'>%s</voice></speak>`,
		lang, ssmlEscaper.Replace(voice), ssmlEscaper.Replace(text),
	)
}

func ssmlLangFromVoice(voice string) string {
	parts := strings.Split(strings.TrimSpace(voice), "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}
