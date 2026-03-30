package tts

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// ProviderBuilder creates a TTS provider instance from configuration.
// It returns the provider and the sample rate.
type ProviderBuilder func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (Provider, int)

// Factory is a registry for TTS providers.
type Factory struct {
	builders map[string]ProviderBuilder
}

// NewFactory creates a new TTS provider factory.
func NewFactory() *Factory {
	return &Factory{
		builders: make(map[string]ProviderBuilder),
	}
}

// Register adds a new TTS provider builder to the factory.
func (f *Factory) Register(name string, builder ProviderBuilder) {
	f.builders[strings.ToLower(name)] = builder
}

// Create instantiates a TTS provider by name.
func (f *Factory) Create(cfg *config.Config, lkCfg config.LiveKitServiceConfig) (Provider, int) {
	providerName := strings.ToLower(strings.TrimSpace(lkCfg.TTS.Provider))
	if providerName == "" {
		providerName = "elevenlabs" // default
	}

	builder, exists := f.builders[providerName]
	if !exists {
		return nil, ParsePCMOutputSampleRate(lkCfg.TTS.OutputFormat)
	}
	return builder(cfg, lkCfg.TTS)
}

// ParsePCMOutputSampleRate extracts the sample rate from a PCM format string like "pcm_24000".
func ParsePCMOutputSampleRate(format string) int {
	format = strings.TrimSpace(format)
	if format == "" {
		return 24000
	}
	if strings.HasPrefix(format, "pcm_") {
		value := strings.TrimPrefix(format, "pcm_")
		switch value {
		case "16000":
			return 16000
		case "22050":
			return 22050
		case "24000":
			return 24000
		case "44100":
			return 44100
		case "48000":
			return 48000
		}
	}
	return 24000 // default
}
