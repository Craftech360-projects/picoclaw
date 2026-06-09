package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNormalizeLiveKitStartupWorkspaceForGOOS_EmptyWorkspaceGetsDefault(t *testing.T) {
	t.Setenv(config.EnvHome, "/tmp/picoclaw-home")

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = ""

	changed, original, normalized, reason := normalizeLiveKitStartupWorkspaceForGOOS(cfg, "linux")
	if !changed {
		t.Fatalf("expected workspace normalization for empty workspace")
	}
	if original != "" {
		t.Fatalf("original workspace = %q, want empty", original)
	}
	want := filepath.Join("/tmp/picoclaw-home", "workspace")
	if normalized != want {
		t.Fatalf("normalized workspace = %q, want %q", normalized, want)
	}
	if cfg.Agents.Defaults.Workspace != want {
		t.Fatalf("cfg workspace = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
	if reason != "empty_workspace" {
		t.Fatalf("reason = %q, want %q", reason, "empty_workspace")
	}
}

func TestNormalizeLiveKitStartupWorkspaceForGOOS_WindowsPathOnLinuxGetsDefault(t *testing.T) {
	t.Setenv(config.EnvHome, "/tmp/picoclaw-home")

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = `C:\\Users\\rahul\\.picoclaw\\workspace`

	changed, original, normalized, reason := normalizeLiveKitStartupWorkspaceForGOOS(cfg, "linux")
	if !changed {
		t.Fatalf("expected workspace normalization for Windows path on linux")
	}
	if original != `C:\\Users\\rahul\\.picoclaw\\workspace` {
		t.Fatalf("original workspace = %q", original)
	}
	want := filepath.Join("/tmp/picoclaw-home", "workspace")
	if normalized != want {
		t.Fatalf("normalized workspace = %q, want %q", normalized, want)
	}
	if cfg.Agents.Defaults.Workspace != want {
		t.Fatalf("cfg workspace = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
	if reason != "windows_absolute_path_on_non_windows" {
		t.Fatalf("reason = %q, want %q", reason, "windows_absolute_path_on_non_windows")
	}
}

func TestNormalizeLiveKitStartupWorkspaceForGOOS_LinuxPathUnchanged(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = "/var/lib/picoclaw/workspace"

	changed, original, normalized, reason := normalizeLiveKitStartupWorkspaceForGOOS(cfg, "linux")
	if changed {
		t.Fatalf("did not expect workspace normalization")
	}
	if original != "/var/lib/picoclaw/workspace" || normalized != "/var/lib/picoclaw/workspace" {
		t.Fatalf("original=%q normalized=%q", original, normalized)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

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

func TestApplyLiveKitRuntimeEnvOverrides(t *testing.T) {
	t.Setenv("PICOCLAW_LIVEKIT_RUNTIME_VAD_THRESHOLD", "0.72")
	t.Setenv("PICOCLAW_LIVEKIT_RUNTIME_VAD_ENDPOINT_MS", "800")
	t.Setenv("PICOCLAW_LIVEKIT_RUNTIME_DETAILED_TRACE_ENABLED", "true")
	t.Setenv("PICOCLAW_LIVEKIT_RUNTIME_TRACE_SAMPLE_RATE", "0.25")

	rt := config.LiveKitServiceRuntimeConfig{
		VADThreshold:  0.68,
		VADEndpointMS: 500,
	}
	applyLiveKitRuntimeEnvOverrides(&rt)

	if rt.VADThreshold != 0.72 {
		t.Fatalf("VADThreshold = %v, want 0.72", rt.VADThreshold)
	}
	if rt.VADEndpointMS != 800 {
		t.Fatalf("VADEndpointMS = %d, want 800", rt.VADEndpointMS)
	}
	if !rt.DetailedTraceEnabled {
		t.Fatal("DetailedTraceEnabled = false, want true")
	}
	if rt.TraceSampleRate != 0.25 {
		t.Fatalf("TraceSampleRate = %v, want 0.25", rt.TraceSampleRate)
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
