package edge_tts

import (
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
)

// NewBuilder creates a new Edge TTS provider builder. Edge is keyless, so it
// always returns a client.
func NewBuilder() tts.ProviderBuilder {
	return func(cfg *config.Config, ttsConfig config.LiveKitServiceTTSConfig) (tts.Provider, int) {
		return NewEdgeTTS(TTSConfig{VoiceID: ttsConfig.VoiceID}), SampleRate
	}
}
