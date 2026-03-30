package elevenlabs_tts

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new ElevenLabs TTS provider builder.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.Voice.ElevenLabsAPIKey,
			VoiceID:      ttsConfig.VoiceID,
			ModelID:      ttsConfig.ModelID,
			OutputFormat: ttsConfig.OutputFormat,
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" && strings.TrimSpace(providerCfg.VoiceID) != "" {
			client = NewElevenLabsTTS(providerCfg)
		}

		sampleRate := ttsConfig.SampleRateHz
		if sampleRate == 0 {
			sampleRate = tts.ParsePCMOutputSampleRate(providerCfg.OutputFormat)
		}
		return client, sampleRate
	}
}
