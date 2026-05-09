package livekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceReconnectHintRoundTrip(t *testing.T) {
	workspace := t.TempDir()

	if err := RecordWorkspaceReconnectHint(workspace, "owner-a"); err != nil {
		t.Fatalf("RecordWorkspaceReconnectHint() error = %v", err)
	}

	ok, owner := HasRecentWorkspaceReconnectHint(workspace, 30*time.Second)
	if !ok {
		t.Fatal("expected reconnect hint to be considered fresh")
	}
	if owner != "owner-a" {
		t.Fatalf("owner = %q, want owner-a", owner)
	}

	if err := ClearWorkspaceReconnectHint(workspace); err != nil {
		t.Fatalf("ClearWorkspaceReconnectHint() error = %v", err)
	}
	ok, _ = HasRecentWorkspaceReconnectHint(workspace, 30*time.Second)
	if ok {
		t.Fatal("expected reconnect hint to be cleared")
	}
}

func TestWorkspaceReconnectHintStale(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, reconnectHintPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	payload, err := json.MarshalIndent(reconnectHint{
		Owner:       "owner-old",
		RequestedAt: time.Now().Add(-2 * time.Minute).UTC(),
	}, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ok, owner := HasRecentWorkspaceReconnectHint(workspace, 30*time.Second)
	if ok {
		t.Fatal("expected stale reconnect hint to be ignored")
	}
	if owner != "owner-old" {
		t.Fatalf("owner = %q, want owner-old", owner)
	}
}
