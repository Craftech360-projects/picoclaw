package main

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestResolveLiveKitWorkspaceLifecycleMakesManagerBackedDeviceWorkspaceEphemeral(t *testing.T) {
	lifecycle := resolveLiveKitWorkspaceLifecycle(
		"room",
		`{"mac_address":"00:16:3e:ac:b5:38","agent_id":"agent-1"}`,
		config.LiveKitServiceManagerAPIConfig{
			BaseURL:             "http://manager.test/toy",
			SessionStoreEnabled: true,
		},
	)

	if lifecycle.WorkspaceIdentity != "device-00163eacb538" {
		t.Fatalf("WorkspaceIdentity = %q, want device-00163eacb538", lifecycle.WorkspaceIdentity)
	}
	if lifecycle.PreserveWorkspace {
		t.Fatal("manager-backed device workspace should be ephemeral")
	}
	if lifecycle.DeviceMAC != "00:16:3e:ac:b5:38" {
		t.Fatalf("DeviceMAC = %q", lifecycle.DeviceMAC)
	}
}

func TestResolveLiveKitWorkspaceLifecyclePreservesDeviceWorkspaceWithoutManagerPersistence(t *testing.T) {
	lifecycle := resolveLiveKitWorkspaceLifecycle(
		"room",
		`{"mac_address":"00:16:3e:ac:b5:38"}`,
		config.LiveKitServiceManagerAPIConfig{},
	)

	if lifecycle.WorkspaceIdentity != "device-00163eacb538" {
		t.Fatalf("WorkspaceIdentity = %q, want device-00163eacb538", lifecycle.WorkspaceIdentity)
	}
	if !lifecycle.PreserveWorkspace {
		t.Fatal("device workspace should be preserved when manager persistence is disabled")
	}
}

func TestResolveLiveKitWorkspaceLifecycleStillPreservesAgentWorkspace(t *testing.T) {
	lifecycle := resolveLiveKitWorkspaceLifecycle(
		"room",
		`{"agent_id":"agent-1"}`,
		config.LiveKitServiceManagerAPIConfig{
			BaseURL:             "http://manager.test/toy",
			SessionStoreEnabled: true,
		},
	)

	if lifecycle.WorkspaceIdentity != "agent-agent-1" {
		t.Fatalf("WorkspaceIdentity = %q, want agent-agent-1", lifecycle.WorkspaceIdentity)
	}
	if !lifecycle.PreserveWorkspace {
		t.Fatal("non-device persistent agent workspace should remain preserved")
	}
}
