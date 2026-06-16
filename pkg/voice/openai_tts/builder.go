package openai_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates an OpenAI-compatible TTS provider builder.
// Base URL comes from OPENAI_BASE_URL (shared with the openai STT provider), so a
// single env var points both STT and TTS at a local server like speaches.
// ponytail: env-var base URL; thread through Manager-API api_base if per-session needed.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if apiKey == "" {
			apiKey = "local" // speaches ignores the key; OpenAI proper needs the env set
		}

		providerCfg := TTSConfig{
			APIKey:       apiKey,
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			SampleRateHz: ttsConfig.SampleRateHz,
			BaseURL:      strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.VoiceID) != "" {
			client = NewOpenAITTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = defaultSampleRate
		}
		return client, sampleRate
	}
}
