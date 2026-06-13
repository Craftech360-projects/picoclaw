package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestApplyManagerActiveProvidersOverridesLLMAndTTS(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "gpt-5.4"

	applyManagerActiveProviders(cfg, managerActiveProviders{
		LLM: managerActiveLLMProvider{
			ModelName: "voice-llm",
			Model:     "openai/gpt-4o-mini",
			APIBase:   "https://api.openai.com/v1",
			APIKey:    "llm-key",
		},
		TTS: managerActiveTTSProvider{
			Provider:     "elevenlabs",
			VoiceID:      "voice-123",
			ModelID:      "eleven_multilingual_v2",
			OutputFormat: "pcm_24000",
			SampleRateHz: 24000,
			APIKey:       "tts-key",
		},
		STT: managerActiveSTTProvider{
			Provider: "deepgram",
			Model:    "nova-2",
			Language: "en",
		},
	})

	if cfg.Agents.Defaults.ModelName != "voice-llm" {
		t.Fatalf("defaults model_name = %q, want voice-llm", cfg.Agents.Defaults.ModelName)
	}
	found := false
	for _, m := range cfg.ModelList {
		if m != nil && m.ModelName == "voice-llm" {
			found = true
			if m.Model != "openai/gpt-4o-mini" {
				t.Fatalf("model = %q, want openai/gpt-4o-mini", m.Model)
			}
			if m.APIKey() != "llm-key" {
				t.Fatalf("llm API key = %q, want llm-key", m.APIKey())
			}
		}
	}
	if !found {
		t.Fatal("expected voice-llm model in model_list")
	}
	if cfg.LiveKitService.TTS.Provider != "elevenlabs" {
		t.Fatalf("tts provider = %q, want elevenlabs", cfg.LiveKitService.TTS.Provider)
	}
	if cfg.LiveKitService.TTS.VoiceID != "voice-123" {
		t.Fatalf("tts voice_id = %q, want voice-123", cfg.LiveKitService.TTS.VoiceID)
	}
	if cfg.Voice.ElevenLabsAPIKey != "tts-key" {
		t.Fatalf("elevenlabs key = %q, want tts-key", cfg.Voice.ElevenLabsAPIKey)
	}
	if cfg.LiveKitService.STT.Provider != "deepgram" {
		t.Fatalf("stt provider = %q, want deepgram", cfg.LiveKitService.STT.Provider)
	}
}

func TestApplyManagerTTSProviderStoresDeepgramAPIKey(t *testing.T) {
	cfg := config.DefaultConfig()

	applyManagerTTSProvider(cfg, managerActiveTTSProvider{
		Provider:     "deepgram",
		ModelID:      "aura-2-asteria-en",
		OutputFormat: "pcm_24000",
		SampleRateHz: 24000,
		APIKey:       "deepgram-tts-key",
	})

	if cfg.LiveKitService.TTS.Provider != "deepgram" {
		t.Fatalf("tts provider = %q, want deepgram", cfg.LiveKitService.TTS.Provider)
	}
	if cfg.LiveKitService.DeepgramAPIKey() != "deepgram-tts-key" {
		t.Fatalf("deepgram key = %q, want deepgram-tts-key", cfg.LiveKitService.DeepgramAPIKey())
	}
	if cfg.Voice.ElevenLabsAPIKey != "" {
		t.Fatalf("elevenlabs key = %q, want empty", cfg.Voice.ElevenLabsAPIKey)
	}
}

func TestResolveLiveKitProviderConfigForSessionNoManagerURLFallsBackToConfig(t *testing.T) {
	liveKitActiveProvidersCache = managerActiveProvidersCache{}
	cfg := config.DefaultConfig()
	resolved, source, err := resolveLiveKitProviderConfigForSession(cfg, config.LiveKitServiceManagerAPIConfig{})
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if source != "config_json" {
		t.Fatalf("source = %q, want config_json", source)
	}
	if resolved == cfg {
		t.Fatal("expected cloned config instance, got same pointer")
	}
}

func TestApplyManagerLLMProviderCollapsesDuplicateModelNameEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "openrouter", Model: "openrouter/google/gemma-4-31b-it"},
		{ModelName: "openrouter", Model: "groq/openai/gpt-oss-120b"},
		{ModelName: "openrouter", Model: "gemini/gemini-2.0-flash"},
	}
	cfg.Agents.Defaults.ModelName = "openrouter"

	applyManagerLLMProvider(cfg, managerActiveLLMProvider{
		ModelName: "openrouter",
		Model:     "openrouter/google/gemma-4-31b-it",
		APIBase:   "https://openrouter.ai/api/v1",
		APIKey:    "key-123",
	})

	count := 0
	for _, item := range cfg.ModelList {
		if item != nil && item.ModelName == "openrouter" {
			count++
			if item.Model != "openrouter/google/gemma-4-31b-it" {
				t.Fatalf("model = %q, want openrouter/google/gemma-4-31b-it", item.Model)
			}
			if item.APIBase != "https://openrouter.ai/api/v1" {
				t.Fatalf("api_base = %q, want https://openrouter.ai/api/v1", item.APIBase)
			}
			if item.APIKey() != "key-123" {
				t.Fatalf("api_key = %q, want key-123", item.APIKey())
			}
		}
	}
	if count != 1 {
		t.Fatalf("openrouter model_name entry count = %d, want 1", count)
	}
}

func TestApplyManagerLLMProviderEnablesCreateProviderWhenModelListInitiallyEmpty(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = nil
	cfg.Agents.Defaults.ModelName = "openrouter"

	applyManagerLLMProvider(cfg, managerActiveLLMProvider{
		ModelName: "openrouter",
		Model:     "openrouter/google/gemma-4-31b-it",
		APIBase:   "https://openrouter.ai/api/v1",
		APIKey:    "key-xyz",
	})

	if len(cfg.ModelList) != 1 {
		t.Fatalf("model_list length = %d, want 1", len(cfg.ModelList))
	}
	if _, _, err := providers.CreateProvider(cfg); err != nil {
		t.Fatalf("CreateProvider() error = %v, want nil", err)
	}
}
