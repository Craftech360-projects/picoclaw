package sarvam_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
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
			logger.InfoCF("sarvam_tts", "Sarvam TTS initialised", map[string]any{
				"tts_provider":      "sarvam",
				"tts_model_id":      providerCfg.ModelID,
				"tts_voice_id":      providerCfg.VoiceID,
				"tts_language_code": providerCfg.LanguageCode,
			})
		} else {
			logger.WarnCF("sarvam_tts", "Sarvam TTS not initialised: no API key (set tts_providers.api_key or SARVAM_API_KEY env) — TTS will be silent", map[string]any{
				"tts_provider": "sarvam",
				"tts_voice_id": providerCfg.VoiceID,
			})
		}

		sampleRate := providerCfg.SampleRateHz
		if sampleRate == 0 {
			sampleRate = defaultSampleRate
		}
		return client, sampleRate
	}
}
