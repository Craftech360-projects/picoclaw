package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildManagerSessionStoreUsesEnvEnablementFallback(t *testing.T) {
	t.Setenv("PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED", "true")
	t.Setenv("MANAGER_API_URL", "http://manager.test/toy")
	t.Setenv("MANAGER_API_SECRET", "test-secret")

	store := buildManagerSessionStore(
		config.LiveKitServiceConfig{},
		"28:56:2F:07:D3:AC",
		"agent-1",
		"session-1",
	)
	if store == nil {
		t.Fatal("buildManagerSessionStore returned nil with env enablement fallback")
	}
}

func TestBuildManagerSessionStoreUsesConfigEnablement(t *testing.T) {
	t.Setenv("MANAGER_API_SECRET", "test-secret")

	store := buildManagerSessionStore(
		config.LiveKitServiceConfig{
			ManagerAPI: config.LiveKitServiceManagerAPIConfig{
				BaseURL:             "http://manager.test/toy",
				SessionStoreEnabled: true,
			},
		},
		"28:56:2F:07:D3:AC",
		"agent-1",
		"session-1",
	)
	if store == nil {
		t.Fatal("buildManagerSessionStore returned nil with config enablement")
	}
}

func TestBuildManagerSessionStoreDisabledByDefault(t *testing.T) {
	store := buildManagerSessionStore(
		config.LiveKitServiceConfig{},
		"28:56:2F:07:D3:AC",
		"agent-1",
		"session-1",
	)
	if store != nil {
		t.Fatal("buildManagerSessionStore returned store when disabled")
	}
}

func TestBuildManagerSessionStoreRequiresDeviceMACAndSessionID(t *testing.T) {
	cfg := config.LiveKitServiceConfig{
		ManagerAPI: config.LiveKitServiceManagerAPIConfig{
			BaseURL:             "http://manager.test/toy",
			SessionStoreEnabled: true,
		},
	}
	t.Setenv("MANAGER_API_SECRET", "test-secret")

	if store := buildManagerSessionStore(cfg, "", "agent-1", "session-1"); store != nil {
		t.Fatal("buildManagerSessionStore returned store without device MAC")
	}
	if store := buildManagerSessionStore(cfg, "28:56:2F:07:D3:AC", "agent-1", ""); store != nil {
		t.Fatal("buildManagerSessionStore returned store without session ID")
	}
}

func TestManagerAPIBaseURLPrefersEnvOverConfig(t *testing.T) {
	t.Setenv("MANAGER_API_URL", "http://env-primary.test/toy")

	got := managerAPIBaseURL(config.LiveKitServiceManagerAPIConfig{
		BaseURL: "http://config.test/toy",
	})
	if got != "http://env-primary.test/toy" {
		t.Fatalf("managerAPIBaseURL() = %q, want env primary", got)
	}
}
