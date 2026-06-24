package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDownloadWorkspaceFilesWritesCanonicalFilesAndPreservesMemoryMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"AGENT.md": map[string]any{
					"content":   "# Agent\n\nDevice prompt.\n",
					"updatedAt": "2026-05-05T00:00:00Z",
				},
				"MEMORY.md": map[string]any{
					"content":   "# Memory\n\nPersistent memory.\n",
					"updatedAt": "2026-05-05T00:00:00Z",
				},
			},
		})
	}))
	defer server.Close()

	workspace := t.TempDir()
	cfg := config.LiveKitServiceManagerAPIConfig{BaseURL: server.URL}
	if err := downloadWorkspaceFiles(context.Background(), cfg, "3c:0f:02:d3:6a:e8", workspace); err != nil {
		t.Fatalf("downloadWorkspaceFiles returned error: %v", err)
	}

	// AGENT.md is session-regenerated (ADR-0003): the download/restore path must
	// NOT write the Manager's stored copy over the freshly-rendered file.
	if _, err := os.Stat(filepath.Join(workspace, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatalf("AGENT.md should be skipped on restore, but it exists (err = %v)", err)
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("Stat(memory/MEMORY.md) error = %v", err)
	}
	assertFilePerm(t, memoryPath, 0o600)
}

func TestUploadWorkspaceFilesQueuesOutboxWhenWorkspaceSyncFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/workspace-sync"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"msg":"sync down"}`))
		case strings.HasSuffix(r.URL.Path, "/workspace-files"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENT.md"), []byte("agent"), 0o644); err != nil {
		t.Fatalf("write AGENT.md: %v", err)
	}
	cfg := config.LiveKitServiceManagerAPIConfig{
		BaseURL: server.URL,
		WorkspaceSync: config.LiveKitWorkspaceSyncConfig{
			Enabled: true,
		},
	}

	if err := uploadWorkspaceFiles(context.Background(), cfg, "3c:0f:02:d3:6a:e8", workspace); err != nil {
		t.Fatalf("uploadWorkspaceFiles returned error: %v", err)
	}

	outbox, err := listWorkspaceSyncOutbox(workspace)
	if err != nil {
		t.Fatalf("listWorkspaceSyncOutbox: %v", err)
	}
	if len(outbox) == 0 {
		t.Fatalf("expected outbox payload after workspace-sync failure")
	}
}

func TestReplayWorkspaceSyncOutboxRemovesQueuedPayloadOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/workspace-sync") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{},
		})
	}))
	defer server.Close()

	workspace := t.TempDir()
	cfg := config.LiveKitServiceManagerAPIConfig{
		BaseURL: server.URL,
		WorkspaceSync: config.LiveKitWorkspaceSyncConfig{
			Enabled: true,
		},
	}

	payload := []byte(`{"baseRevision":"1","newRevision":"2","files":[]}`)
	if err := queueWorkspaceSyncOutbox(workspace, payload, "unit-test"); err != nil {
		t.Fatalf("queueWorkspaceSyncOutbox: %v", err)
	}

	replayed, err := replayWorkspaceSyncOutbox(context.Background(), cfg, "3c:0f:02:d3:6a:e8", workspace)
	if err != nil {
		t.Fatalf("replayWorkspaceSyncOutbox: %v", err)
	}
	if replayed != 1 {
		t.Fatalf("replayed = %d, want 1", replayed)
	}
	outbox, err := listWorkspaceSyncOutbox(workspace)
	if err != nil {
		t.Fatalf("listWorkspaceSyncOutbox: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("expected empty outbox, got %d entries", len(outbox))
	}
}

func TestReplayWorkspaceSyncOutboxDiscardsLegacyBinaryPayloadOn400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/workspace-sync") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"msg":"skills/foo.mp3 contains unsupported binary null bytes"}`))
	}))
	defer server.Close()

	workspace := t.TempDir()
	cfg := config.LiveKitServiceManagerAPIConfig{
		BaseURL: server.URL,
		WorkspaceSync: config.LiveKitWorkspaceSyncConfig{
			Enabled: true,
		},
	}

	payload := []byte(`{"baseRevision":"1","newRevision":"2","files":[{"relativePath":"skills/foo.mp3","content":"bad"}]}`)
	if err := queueWorkspaceSyncOutbox(workspace, payload, "unit-test"); err != nil {
		t.Fatalf("queueWorkspaceSyncOutbox: %v", err)
	}

	replayed, err := replayWorkspaceSyncOutbox(context.Background(), cfg, "3c:0f:02:d3:6a:e8", workspace)
	if err != nil {
		t.Fatalf("replayWorkspaceSyncOutbox: %v", err)
	}
	if replayed != 0 {
		t.Fatalf("replayed = %d, want 0", replayed)
	}
	outbox, err := listWorkspaceSyncOutbox(workspace)
	if err != nil {
		t.Fatalf("listWorkspaceSyncOutbox: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("expected empty outbox after discard, got %d entries", len(outbox))
	}
}

func TestIsWorkspaceSyncExcluded_DeviceLock(t *testing.T) {
	if !isWorkspaceSyncExcluded(".picoclaw/device.lock", nil) {
		t.Fatalf("expected .picoclaw/device.lock to be excluded")
	}
}

func TestIsSessionRegeneratedCoreFile(t *testing.T) {
	regenerated := []string{"AGENT.md", "SOUL.md", "agent.md", "soul.md", "./AGENT.md"}
	for _, name := range regenerated {
		if !isSessionRegeneratedCoreFile(name) {
			t.Errorf("isSessionRegeneratedCoreFile(%q) = false, want true", name)
		}
	}
	notRegenerated := []string{"USER.md", "MEMORY.md", "HEARTBEAT.md", "memory/MEMORY.md", "user.md"}
	for _, name := range notRegenerated {
		if isSessionRegeneratedCoreFile(name) {
			t.Errorf("isSessionRegeneratedCoreFile(%q) = true, want false", name)
		}
	}
}

// TestWorkspaceSyncRestorePreservesSessionRegeneratedFiles asserts the
// workspace-sync DOWNLOAD/RESTORE path does not overwrite the freshly-rendered
// AGENT.md/SOUL.md (ADR-0003) while still restoring USER.md/MEMORY.md.
func TestWorkspaceSyncRestorePreservesSessionRegeneratedFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"revision": "10",
				"files": []map[string]any{
					{"relativePath": "AGENT.md", "content": "# OLD Manager Agent\n"},
					{"relativePath": "SOUL.md", "content": "# OLD Manager Soul\n"},
					{"relativePath": "USER.md", "content": "# User\n"},
					{"relativePath": "memory/MEMORY.md", "content": "# Memory\n"},
				},
			},
		})
	}))
	defer server.Close()

	workspace := t.TempDir()
	// Simulate the per-session render already on disk.
	renderedAgent := "# Rendered Tenali Agent\n"
	renderedSoul := "# Rendered Tenali Soul\n"
	if err := os.WriteFile(filepath.Join(workspace, "AGENT.md"), []byte(renderedAgent), 0o644); err != nil {
		t.Fatalf("write AGENT.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte(renderedSoul), 0o644); err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}

	cfg := config.LiveKitServiceManagerAPIConfig{
		BaseURL:       server.URL,
		WorkspaceSync: config.LiveKitWorkspaceSyncConfig{Enabled: true},
	}
	if err := tryDownloadWorkspaceSync(context.Background(), cfg, "3c:0f:02:d3:6a:e8", workspace); err != nil {
		t.Fatalf("tryDownloadWorkspaceSync: %v", err)
	}

	gotAgent, err := os.ReadFile(filepath.Join(workspace, "AGENT.md"))
	if err != nil {
		t.Fatalf("read AGENT.md: %v", err)
	}
	if string(gotAgent) != renderedAgent {
		t.Errorf("AGENT.md was overwritten: got %q, want %q", string(gotAgent), renderedAgent)
	}
	gotSoul, err := os.ReadFile(filepath.Join(workspace, "SOUL.md"))
	if err != nil {
		t.Fatalf("read SOUL.md: %v", err)
	}
	if string(gotSoul) != renderedSoul {
		t.Errorf("SOUL.md was overwritten: got %q, want %q", string(gotSoul), renderedSoul)
	}

	// USER.md and MEMORY.md must still be restored from the Manager.
	if _, err := os.Stat(filepath.Join(workspace, "USER.md")); err != nil {
		t.Errorf("USER.md should be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "memory", "MEMORY.md")); err != nil {
		t.Errorf("memory/MEMORY.md should be restored: %v", err)
	}
}
