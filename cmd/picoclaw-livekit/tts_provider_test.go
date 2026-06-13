package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildTTSProviderCreatesDeepgramProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LiveKitService.SetDeepgramAPIKey("deepgram-key")
	cfg.LiveKitService.TTS.Provider = "deepgram"
	cfg.LiveKitService.TTS.ModelID = "aura-2-asteria-en"
	cfg.LiveKitService.TTS.OutputFormat = "pcm_24000"

	provider, sampleRate := buildTTSProvider(cfg, cfg.LiveKitService)
	if provider == nil {
		t.Fatal("provider = nil, want Deepgram TTS provider")
	}
	if sampleRate != 24000 {
		t.Fatalf("sampleRate = %d, want 24000", sampleRate)
	}
}
