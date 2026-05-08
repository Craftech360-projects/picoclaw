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

	if _, err := os.ReadFile(filepath.Join(workspace, "AGENT.md")); err != nil {
		t.Fatalf("ReadFile(AGENT.md) error = %v", err)
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("Stat(memory/MEMORY.md) error = %v", err)
	}
	info, err := os.Stat(memoryPath)
	if err != nil {
		t.Fatalf("Stat(memory/MEMORY.md) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("memory/MEMORY.md mode = %v, want 0600", got)
	}
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
