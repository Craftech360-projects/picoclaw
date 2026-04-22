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
