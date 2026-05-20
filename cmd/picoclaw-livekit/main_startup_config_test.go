package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestValidateLiveKitStartupConfigFilesStrictSuccess(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")
	secPath := filepath.Join(tmp, config.SecurityConfigFile)

	if err := os.WriteFile(cfgPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(secPath, []byte("model_list: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(security) error = %v", err)
	}

	if err := validateLiveKitStartupConfigFiles(cfgPath); err != nil {
		t.Fatalf("validateLiveKitStartupConfigFiles() error = %v", err)
	}
}

func TestValidateLiveKitStartupConfigFilesMissingSecurity(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	if err := validateLiveKitStartupConfigFiles(cfgPath); err == nil {
		t.Fatalf("validateLiveKitStartupConfigFiles() expected error when .security.yml is missing")
	}
}

func TestValidateLiveKitStartupCredentialsStrict(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LiveKitService.ServerURL = "wss://example.livekit.cloud"

	if err := validateLiveKitStartupCredentials(cfg, true); err == nil {
		t.Fatalf("validateLiveKitStartupCredentials() expected strict credentials error")
	}

	cfg.LiveKitService.SetAPIKey("key")
	cfg.LiveKitService.SetAPISecret("secret")
	if err := validateLiveKitStartupCredentials(cfg, true); err != nil {
		t.Fatalf("validateLiveKitStartupCredentials() unexpected error after keys set: %v", err)
	}
}

func TestValidateLiveKitStartupCredentialsStrictManagerNeedsServiceKey(t *testing.T) {
	t.Setenv("PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY", "")
	t.Setenv("SERVICE_SECRET_KEY", "")
	t.Setenv("MANAGER_API_SECRET", "")

	cfg := config.DefaultConfig()
	cfg.LiveKitService.ServerURL = "wss://example.livekit.cloud"
	cfg.LiveKitService.SetAPIKey("key")
	cfg.LiveKitService.SetAPISecret("secret")
	cfg.LiveKitService.ManagerAPI.BaseURL = "https://manager.example.com/toy"

	if err := validateLiveKitStartupCredentials(cfg, true); err == nil {
		t.Fatalf("validateLiveKitStartupCredentials() expected manager service key error in strict mode")
	}
}
