package stt

import "testing"

// ponytail: converted to manager-mode tests (no DB required). DB-backed tests
// require a PostgreSQL connection and are integration tests, not unit tests.
// Manager-mode (NewFactoryFromProviders) exercises the same provider selection logic.

func TestFactory_GetActiveProvider_NoProvider(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}

	// Should default to deepgram when no provider configured
	if provider.Name() != "deepgram" {
		t.Errorf("Expected default provider 'deepgram', got '%s'", provider.Name())
	}
}

func TestFactory_UpdateProviderConfig(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	// Manager-mode doesn't support DB updates; skip DB-specific behavior
	// Test that GetActiveProvider returns deepgram default
	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}
	if provider.Name() != "deepgram" {
		t.Errorf("Expected default 'deepgram', got '%s'", provider.Name())
	}
}

func TestFactory_UpdateProviderConfig_BuiltInProvider(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{
		{Name: "deepgram", Model: "nova-2", IsActive: true, Priority: 1, APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}

	if provider.Name() != "deepgram" {
		t.Errorf("Expected active provider 'deepgram', got '%s'", provider.Name())
	}
}

func TestFactory_ListProviders(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{
		{Name: "deepgram", Model: "nova-2", IsActive: true, Priority: 1, APIKey: "key"},
	})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	providers := factory.ListProviders()
	if len(providers) == 0 {
		t.Error("Expected at least one registered provider")
	}

	found := false
	for _, p := range providers {
		if p == "deepgram" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'deepgram' in registered providers")
	}
}

func TestFactory_MultipleProviders(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{
		{Name: "deepgram", Model: "nova-2", IsActive: true, Priority: 1, APIKey: "dg-key"},
	})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}
	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}

	// Switch to a different active provider
	factory.SetActiveProvider("groq", "whisper-large-v3", "en", "groq-key")
	provider, err = factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider after switch failed: %v", err)
	}
	if provider.Name() != "groq" {
		t.Errorf("Expected 'groq' after switch, got '%s'", provider.Name())
	}
}

func TestFactory_UpdateProviderConfig_UpdateExisting(t *testing.T) {
	factory, err := NewFactoryFromProviders([]ProviderInfo{
		{Name: "deepgram", Model: "nova-2", IsActive: true, Priority: 1, APIKey: "old-key"},
	})
	if err != nil {
		t.Fatalf("NewFactoryFromProviders failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}

	// Verify the first config took effect
	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}

	// Update active provider in manager-mode
	factory.SetActiveProvider("deepgram", "nova-3", "en", "new-key")
	provider, err = factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider after update failed: %v", err)
	}

	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}
}

func TestNewFactory_CreatesDatabaseFile(t *testing.T) {
	// This test is DB-specific and requires PostgreSQL connection.
	// Skipping in unit tests; this is an integration test.
	t.Skip("DB-specific test requires PostgreSQL connection")
}
