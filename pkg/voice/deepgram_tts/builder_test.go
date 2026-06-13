package deepgram_tts

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuilderUsesDeepgramEnvFallback(t *testing.T) {
	t.Setenv("DEEPGRAM_API_KEY", "env-deepgram-key")

	cfg := config.DefaultConfig()
	cfg.LiveKitService.TTS.Provider = "deepgram"
	cfg.LiveKitService.TTS.ModelID = "aura-2-asteria-en"

	provider, sampleRate := NewBuilder()(cfg, cfg.LiveKitService.TTS)
	if provider == nil {
		t.Fatal("provider = nil, want Deepgram TTS provider from DEEPGRAM_API_KEY")
	}
	if sampleRate != 24000 {
		t.Fatalf("sampleRate = %d, want 24000", sampleRate)
	}
}
