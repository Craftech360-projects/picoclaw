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

func TestBuildTTSProviderCreatesSarvamProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LiveKitService.SetSarvamAPIKey("sarvam-key")
	cfg.LiveKitService.TTS.Provider = "sarvam"
	cfg.LiveKitService.TTS.VoiceID = "meera"
	cfg.LiveKitService.TTS.SampleRateHz = 22050
	cfg.LiveKitService.TTS.Language = "ta-IN"

	provider, sampleRate := buildTTSProvider(cfg, cfg.LiveKitService)
	if provider == nil {
		t.Fatal("provider = nil, want Sarvam TTS provider")
	}
	if sampleRate != 22050 {
		t.Fatalf("sampleRate = %d, want 22050", sampleRate)
	}
}

func TestBuildTTSProviderCreatesEdgeProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LiveKitService.TTS.Provider = "edge" // keyless
	cfg.LiveKitService.TTS.VoiceID = "en-US-AnaNeural"

	provider, sampleRate := buildTTSProvider(cfg, cfg.LiveKitService)
	if provider == nil {
		t.Fatal("provider = nil, want Edge TTS provider")
	}
	if sampleRate != 24000 {
		t.Fatalf("sampleRate = %d, want 24000", sampleRate)
	}
}

func TestBuildTTSProviderCreatesAzureProvider(t *testing.T) {
	t.Setenv("AZURE_SPEECH_REGION", "eastus")
	cfg := config.DefaultConfig()
	cfg.LiveKitService.SetAzureAPIKey("azure-key")
	cfg.LiveKitService.TTS.Provider = "azure"
	cfg.LiveKitService.TTS.VoiceID = "en-US-AnaNeural"
	cfg.LiveKitService.TTS.SampleRateHz = 24000

	provider, sampleRate := buildTTSProvider(cfg, cfg.LiveKitService)
	if provider == nil {
		t.Fatal("provider = nil, want Azure TTS provider")
	}
	if sampleRate != 24000 {
		t.Fatalf("sampleRate = %d, want 24000", sampleRate)
	}
}
