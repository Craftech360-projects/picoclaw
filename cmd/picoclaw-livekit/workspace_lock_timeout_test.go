package main

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestLiveKitWorkspaceLockTimeout_Default(t *testing.T) {
	t.Setenv("PICOCLAW_LIVEKIT_WORKSPACE_LOCK_TIMEOUT_SECONDS", "")
	got := liveKitWorkspaceLockTimeout(config.LiveKitServiceManagerAPIConfig{})
	if got != 30*time.Second {
		t.Fatalf("default lock timeout = %s, want %s", got, 30*time.Second)
	}
}

func TestLiveKitWorkspaceLockTimeout_ConfigOverride(t *testing.T) {
	cfg := config.LiveKitServiceManagerAPIConfig{
		WorkspaceSync: config.LiveKitWorkspaceSyncConfig{
			LockTimeoutSecond: 12,
		},
	}
	got := liveKitWorkspaceLockTimeout(cfg)
	if got != 12*time.Second {
		t.Fatalf("config lock timeout = %s, want %s", got, 12*time.Second)
	}
}

