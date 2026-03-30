package cartesia_tts

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Cartesia TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.CartesiaAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			SampleRateHz: ttsConfig.SampleRateHz,
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" && strings.TrimSpace(providerCfg.VoiceID) != "" {
			client = NewCartesiaTTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = 24000 // Cartesia's default
		}
		return client, sampleRate
	}
}
