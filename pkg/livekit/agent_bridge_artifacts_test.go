package livekit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type fakeWorkspaceArtifactStore struct {
	saved WorkspaceArtifact
}

func (f *fakeWorkspaceArtifactStore) SaveArtifact(_ context.Context, artifact WorkspaceArtifact) error {
	f.saved = artifact
	return nil
}

func (f *fakeWorkspaceArtifactStore) ListArtifacts(_ context.Context, _ int) ([]WorkspaceArtifact, error) {
	return nil, nil
}

func TestExecuteToolMirrorsSuccessfulWriteFileToArtifactStore(t *testing.T) {
	workspace := t.TempDir()
	registry := tools.NewToolRegistry()
	registry.Register(tools.NewWriteFileTool(workspace, true))
	store := &fakeWorkspaceArtifactStore{}

	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
			Tools:     registry,
		},
		tools:              registry,
		workspaceArtifacts: store,
	}

	result := bridge.executeTool(context.Background(), "session-1", providers.ToolCall{
		Name: "write_file",
		Arguments: map[string]any{
			"path":      filepath.Join(workspace, "notes", "flower_song.txt"),
			"content":   "petals and sunshine",
			"overwrite": true,
		},
	})

	if result == nil || result.IsError {
		t.Fatalf("write_file result = %#v, want success", result)
	}
	if store.saved.SessionID != "session-1" {
		t.Fatalf("saved SessionID = %q, want session-1", store.saved.SessionID)
	}
	if store.saved.RelativePath != "notes/flower_song.txt" {
		t.Fatalf("saved RelativePath = %q, want notes/flower_song.txt", store.saved.RelativePath)
	}
	if store.saved.Content != "petals and sunshine" {
		t.Fatalf("saved Content = %q", store.saved.Content)
	}
	if store.saved.ContentType != "text/plain" {
		t.Fatalf("saved ContentType = %q, want text/plain", store.saved.ContentType)
	}
}

func TestArtifactRelativePathRejectsWorkspaceEscapes(t *testing.T) {
	workspace := t.TempDir()
	if _, ok := artifactRelativePath(workspace, filepath.Join(workspace, "..", "secret.txt")); ok {
		t.Fatal("artifactRelativePath accepted a path outside the workspace")
	}
}
