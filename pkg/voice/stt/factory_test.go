package stt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFactory_GetActiveProvider_NoProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

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
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

	err = factory.UpdateProviderConfig("groq", "test-key", "whisper-large-v3", true, 5)
	if err != nil {
		t.Fatalf("UpdateProviderConfig failed: %v", err)
	}

	// groq is not registered as a built-in, so GetActiveProvider should
	// return the "provider not registered" error. This is expected behavior
	// -- the DB can reference providers that haven't been implemented yet.
	provider, err := factory.GetActiveProvider()
	if err == nil {
		t.Fatalf("Expected error for unregistered provider, got provider '%s'", provider.Name())
	}
}

func TestFactory_UpdateProviderConfig_BuiltInProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

	// Configure deepgram (a built-in provider) as active
	err = factory.UpdateProviderConfig("deepgram", "test-key", "nova-2", true, 1)
	if err != nil {
		t.Fatalf("UpdateProviderConfig failed: %v", err)
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
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

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
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

	// Seed deepgram as active
	err = factory.UpdateProviderConfig("deepgram", "dg-key", "nova-2", true, 1)
	if err != nil {
		t.Fatalf("UpdateProviderConfig deepgram failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider failed: %v", err)
	}
	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}

	// Switch active flag away from deepgram
	err = factory.UpdateProviderConfig("deepgram", "dg-key", "nova-2", false, 1)
	if err != nil {
		t.Fatalf("Deactivate deepgram failed: %v", err)
	}

	// Now no provider is active, so default to deepgram again
	provider, err = factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider after deactivation failed: %v", err)
	}
	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram' as default, got '%s'", provider.Name())
	}
}

func TestFactory_UpdateProviderConfig_UpdateExisting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

	// Insert
	err = factory.UpdateProviderConfig("deepgram", "old-key", "nova-2", true, 1)
	if err != nil {
		t.Fatalf("First UpdateProviderConfig failed: %v", err)
	}

	provider, err := factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider after insert failed: %v", err)
	}

	// Verify the first config took effect
	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}

	// Update with different model
	err = factory.UpdateProviderConfig("deepgram", "new-key", "nova-3", true, 2)
	if err != nil {
		t.Fatalf("Second UpdateProviderConfig failed: %v", err)
	}

	provider, err = factory.GetActiveProvider()
	if err != nil {
		t.Fatalf("GetActiveProvider after update failed: %v", err)
	}

	if provider.Name() != "deepgram" {
		t.Errorf("Expected 'deepgram', got '%s'", provider.Name())
	}
}

func TestNewFactory_CreatesDatabaseFile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("DB should not exist yet at %s", dbPath)
	}

	factory, err := NewFactory(dbPath)
	if err != nil {
		t.Fatalf("NewFactory failed: %v", err)
	}
	defer factory.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("DB file should exist after NewFactory")
	}
}
