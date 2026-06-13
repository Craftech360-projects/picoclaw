package deepgram_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Deepgram TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.DeepgramAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			OutputFormat: ttsConfig.OutputFormat,
			SampleRateHz: ttsConfig.SampleRateHz,
		}
		if strings.TrimSpace(providerCfg.APIKey) == "" {
			providerCfg.APIKey = os.Getenv("DEEPGRAM_API_KEY")
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" {
			client = NewDeepgramTTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = parseSampleRate(providerCfg.OutputFormat)
		}
		return client, sampleRate
	}
}
