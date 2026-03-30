package inworld_tts

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Inworld TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.InworldAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			SampleRateHz: ttsConfig.SampleRateHz,
			Temperature:  ttsConfig.Temperature,
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" &&
			strings.TrimSpace(providerCfg.VoiceID) != "" &&
			strings.TrimSpace(providerCfg.ModelID) != "" {
			client = NewInworldTTS(providerCfg)
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = 22050 // Inworld's default
		}
		return client, sampleRate
	}
}
