package smallest_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new SmallestAI TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.SmallestAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			OutputFormat: ttsConfig.OutputFormat,
			SampleRateHz: ttsConfig.SampleRateHz,
		}
		if strings.TrimSpace(providerCfg.APIKey) == "" {
			providerCfg.APIKey = os.Getenv("SMALLEST_API_KEY")
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" {
			client = NewSmallestTTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = parseSampleRate(providerCfg.OutputFormat)
		}
		return client, sampleRate
	}
}
