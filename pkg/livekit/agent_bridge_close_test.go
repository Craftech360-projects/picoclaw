package livekit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
)

type fakeBridgeCloser struct {
	closed bool
}

func (f *fakeBridgeCloser) Close() error {
	f.closed = true
	return nil
}

func TestAgentBridgeCloseDeletesWorkspaceByDefault(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace-default")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	ab := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
		},
	}
	ab.Close()

	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace should be removed, stat err = %v", err)
	}
}

func TestAgentBridgeClosePreservesWorkspaceWhenConfigured(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace-preserve")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	ab := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
		},
		preserveWorkspace: true,
	}
	ab.Close()

	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace should be preserved, stat err = %v", err)
	}
}

func TestAgentBridgeCloseClosesMCPManager(t *testing.T) {
	closer := &fakeBridgeCloser{}

	ab := &AgentBridge{
		agentInstance: &agent.AgentInstance{},
		mcpManager:    closer,
	}
	ab.Close()

	if !closer.closed {
		t.Fatal("expected AgentBridge.Close to close MCP manager")
	}
}

func TestAgentBridgeCloseSkipsDeleteWhenReconnectHintIsFresh(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace-handoff")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := RecordWorkspaceReconnectHint(workspace, "owner-reconnect"); err != nil {
		t.Fatalf("RecordWorkspaceReconnectHint() error = %v", err)
	}

	ab := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
		},
	}
	ab.Close()

	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("workspace should be preserved for reconnect handoff, stat err = %v", err)
	}
}

func TestAgentBridgeCloseCallsOnAfterClose(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace-callback")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	onAfterCalled := false
	ab := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
		},
		onClose: func() {
			// simulate lightweight close phase
			time.Sleep(5 * time.Millisecond)
		},
		onAfterClose: func() {
			onAfterCalled = true
		},
	}
	ab.Close()

	if !onAfterCalled {
		t.Fatal("expected onAfterClose callback to be called")
	}
}
