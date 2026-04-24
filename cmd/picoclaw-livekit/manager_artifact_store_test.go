package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	picokit "github.com/sipeed/picoclaw/pkg/livekit"
)

type fakeHydrationArtifactStore struct {
	artifacts []picokit.WorkspaceArtifact
}

func (f fakeHydrationArtifactStore) SaveArtifact(context.Context, picokit.WorkspaceArtifact) error {
	return nil
}

func (f fakeHydrationArtifactStore) ListArtifacts(context.Context, int) ([]picokit.WorkspaceArtifact, error) {
	return f.artifacts, nil
}

func TestHydrateWorkspaceArtifactsWritesSafeRelativeFiles(t *testing.T) {
	workspace := t.TempDir()
	store := fakeHydrationArtifactStore{artifacts: []picokit.WorkspaceArtifact{
		{RelativePath: "flower_song.txt", Content: "petals", ContentType: "text/plain"},
		{RelativePath: "../secret.txt", Content: "nope", ContentType: "text/plain"},
	}}

	written, err := hydrateWorkspaceArtifacts(context.Background(), store, workspace, 10)
	if err != nil {
		t.Fatalf("hydrateWorkspaceArtifacts returned error: %v", err)
	}
	if written != 1 {
		t.Fatalf("written = %d, want 1", written)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "flower_song.txt"))
	if err != nil {
		t.Fatalf("expected hydrated file: %v", err)
	}
	if string(data) != "petals" {
		t.Fatalf("hydrated file = %q, want petals", string(data))
	}
	if _, err := os.Stat(filepath.Join(workspace, "..", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape artifact should not be written, stat err=%v", err)
	}
}

func TestHydrateWorkspaceArtifactsSkipsGeneratedMemoryContextFiles(t *testing.T) {
	workspace := t.TempDir()
	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	contextPath := filepath.Join(workspace, "sessions", "manager_recent_voice_context.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		t.Fatalf("MkdirAll memory: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(contextPath), 0o755); err != nil {
		t.Fatalf("MkdirAll sessions: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("fresh manager memory"), 0o600); err != nil {
		t.Fatalf("WriteFile memory: %v", err)
	}
	if err := os.WriteFile(contextPath, []byte("fresh recent context"), 0o600); err != nil {
		t.Fatalf("WriteFile context: %v", err)
	}

	store := fakeHydrationArtifactStore{artifacts: []picokit.WorkspaceArtifact{
		{RelativePath: "memory/MEMORY.md", Content: "stale artifact memory", ContentType: "text/plain"},
		{RelativePath: "sessions/manager_recent_voice_context.md", Content: "stale artifact context", ContentType: "text/plain"},
		{RelativePath: "flower_song.txt", Content: "petals", ContentType: "text/plain"},
	}}

	written, err := hydrateWorkspaceArtifacts(context.Background(), store, workspace, 10)
	if err != nil {
		t.Fatalf("hydrateWorkspaceArtifacts returned error: %v", err)
	}
	if written != 1 {
		t.Fatalf("written = %d, want 1", written)
	}
	if data, err := os.ReadFile(memoryPath); err != nil || string(data) != "fresh manager memory" {
		t.Fatalf("memory file = %q, err=%v", string(data), err)
	}
	if data, err := os.ReadFile(contextPath); err != nil || string(data) != "fresh recent context" {
		t.Fatalf("context file = %q, err=%v", string(data), err)
	}
}
