package azure_tts

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Azure TTS provider builder. The region is taken from
// the DB row's model_id (Azure ignores model_id otherwise), falling back to the
// AZURE_SPEECH_REGION / AZURE_SPEECH_ENDPOINT env. The subscription key comes
// from the manager DB (api_key) with AZURE_SPEECH_KEY as a fallback.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		providerCfg := TTSConfig{
			APIKey:       cfg.LiveKitService.AzureAPIKey(),
			VoiceID:      ttsConfig.VoiceID,
			Endpoint:     resolveEndpoint(ttsConfig.ModelID),
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
			logger.WarnCF("azure_tts", "Azure TTS not initialised: missing key and/or region — TTS will be silent. Set the region in the DB row's model_id (e.g. 'centralindia'), or via AZURE_SPEECH_REGION/AZURE_SPEECH_ENDPOINT; key via DB api_key or AZURE_SPEECH_KEY", map[string]any{
				"tts_provider": "azure",
				"has_api_key":  strings.TrimSpace(providerCfg.APIKey) != "",
				"has_endpoint": strings.TrimSpace(providerCfg.Endpoint) != "",
			})
		}

		_, sampleRate := azureOutputFormat(providerCfg.SampleRateHz)
		return client, sampleRate
	}
}

// resolveEndpoint builds the synthesis endpoint. Precedence: the DB region
// (model_id, e.g. "centralindia"), then AZURE_SPEECH_ENDPOINT (full URL), then
// AZURE_SPEECH_REGION. Returns "" when no region/endpoint is configured.
func resolveEndpoint(dbRegion string) string {
	if region := strings.TrimSpace(dbRegion); region != "" {
		return endpointForRegion(region)
	}
	if endpoint := strings.TrimSpace(os.Getenv("AZURE_SPEECH_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	if region := strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION")); region != "" {
		return endpointForRegion(region)
	}
	return ""
}

func endpointForRegion(region string) string {
	return "https://" + region + ".tts.speech.microsoft.com/cognitiveservices/v1"
}
