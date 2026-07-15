package azure_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Azure TTS provider builder. Region/endpoint come from
// the environment (AZURE_SPEECH_REGION or AZURE_SPEECH_ENDPOINT); the
// subscription key comes from the manager DB (api_key) with AZURE_SPEECH_KEY as
// a fallback.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.AzureAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			Endpoint:     resolveEndpoint(),
			SampleRateHz: ttsConfig.SampleRateHz,
		}
		if strings.TrimSpace(providerCfg.APIKey) == "" {
			providerCfg.APIKey = os.Getenv("AZURE_SPEECH_KEY")
		}

		var client tts.Provider
		if strings.TrimSpace(providerCfg.APIKey) != "" && strings.TrimSpace(providerCfg.Endpoint) != "" {
			client = NewAzureTTS(providerCfg)
			logger.InfoCF("azure_tts", "Azure TTS initialised", map[string]any{
				"tts_provider": "azure",
				"tts_voice_id": providerCfg.VoiceID,
				"endpoint":     providerCfg.Endpoint,
			})
		} else {
			logger.WarnCF("azure_tts", "Azure TTS not initialised: missing key and/or region — TTS will be silent. Set AZURE_SPEECH_KEY and AZURE_SPEECH_REGION (or AZURE_SPEECH_ENDPOINT)", map[string]any{
				"tts_provider": "azure",
				"has_api_key":  strings.TrimSpace(providerCfg.APIKey) != "",
				"has_endpoint": strings.TrimSpace(providerCfg.Endpoint) != "",
			})
		}

		_, sampleRate := azureOutputFormat(providerCfg.SampleRateHz)
		return client, sampleRate
	}
}

// resolveEndpoint returns the full synthesis endpoint from AZURE_SPEECH_ENDPOINT,
// else builds it from AZURE_SPEECH_REGION, else returns "".
func resolveEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("AZURE_SPEECH_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	region := strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION"))
	if region == "" {
		return ""
	}
	return "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/v1"
}
