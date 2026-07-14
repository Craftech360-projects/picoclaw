package sarvam_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Sarvam TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.SarvamAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			SampleRateHz: ttsConfig.SampleRateHz,
			LanguageCode: ResolveLanguageCode(ttsConfig.Language),
		}
		if strings.TrimSpace(providerCfg.APIKey) == "" {
			providerCfg.APIKey = os.Getenv("SARVAM_API_KEY")
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" {
			client = NewSarvamTTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = defaultSampleRate
		}
		return client, sampleRate
	}
}
