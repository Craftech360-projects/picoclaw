package stt

import "testing"

// Manager-API mode: no DB, provider list supplied in memory.

func TestActiveFromConfigs_PicksHighestPriorityActive(t *testing.T) {
	f, err := NewFactoryFromProviders([]ProviderInfo{
		{Name: "deepgram", Model: "nova-2", IsActive: true, Priority: 1, APIKey: "dg"},
		{Name: "groq", Model: "whisper-large-v3", IsActive: true, Priority: 5, APIKey: "gq"},
		{Name: "openai", Model: "whisper-1", IsActive: false, Priority: 6, APIKey: "oa"}, // inactive, higher priority
	})
	if err != nil {
		t.Fatal(err)
	}
	name, key, model := f.activeFromConfigs()
	if name != "groq" || key != "gq" || model != "whisper-large-v3" {
		t.Fatalf("want groq/gq/whisper-large-v3, got %s/%s/%s", name, key, model)
	}
}

func TestActiveFromConfigs_FallsBackToDeepgramWhenNoneActive(t *testing.T) {
	f, _ := NewFactoryFromProviders([]ProviderInfo{{Name: "groq", IsActive: false, Priority: 5}})
	if name, _, _ := f.activeFromConfigs(); name != "deepgram" {
		t.Fatalf("want deepgram fallback, got %s", name)
	}
}

func TestSetActiveProvider_SwitchesActive(t *testing.T) {
	f, _ := NewFactoryFromProviders([]ProviderInfo{
		{Name: "sarvam", Model: "saaras:v3", IsActive: true, Priority: 1, APIKey: "sv"},
	})
	if name, _, _ := f.activeFromConfigs(); name != "sarvam" {
		t.Fatalf("initial want sarvam, got %s", name)
	}
	f.SetActiveProvider("deepgram", "nova-2", "en", "dg")
	name, key, model := f.activeFromConfigs()
	if name != "deepgram" || key != "dg" || model != "nova-2" {
		t.Fatalf("after switch want deepgram/dg/nova-2, got %s/%s/%s", name, key, model)
	}
}

func TestGetActiveProvider_ManagerMode_NoDBPanic(t *testing.T) {
	f, _ := NewFactoryFromProviders([]ProviderInfo{
		{Name: "groq", Model: "whisper-large-v3", IsActive: true, Priority: 5, APIKey: "gq"},
	})
	p, err := f.GetActiveProvider()
	if err != nil || p == nil {
		t.Fatalf("expected a provider from the in-memory list; err=%v p=%v", err, p)
	}
}
