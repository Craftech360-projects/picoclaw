package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestEnsureLiveKitWorkspaceFileToolsAddsRequiredToolsWhenConfigDisablesThem(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.ReadFile.Enabled = false
	cfg.Tools.WriteFile.Enabled = false
	cfg.Tools.ListDir.Enabled = false
	cfg.Tools.Exec.Enabled = false
	cfg.Tools.Exec.AllowRemote = false
	cfg.Tools.Web.Enabled = false
	cfg.Tools.WebFetch.Enabled = false

	instance := &agent.AgentInstance{
		Workspace: t.TempDir(),
		Tools:     tools.NewToolRegistry(),
	}

	added := ensureLiveKitWorkspaceFileTools(instance, &cfg.Agents.Defaults, cfg)

	for _, name := range []string{"read_file", "write_file", "list_dir"} {
		if _, ok := instance.Tools.Get(name); !ok {
			t.Fatalf("%s should be registered for LiveKit workspace agents", name)
		}
	}
	if _, ok := instance.Tools.Get("exec"); !ok {
		t.Fatal("exec should be registered for LiveKit active-skill agents")
	}
	if _, ok := instance.Tools.Get("web_fetch"); !ok {
		t.Fatal("web_fetch should be registered for LiveKit active-skill agents")
	}
	if _, ok := instance.Tools.Get("web_search"); !ok {
		t.Fatal("web_search should be registered for LiveKit active-skill agents")
	}
	if len(added) != 6 {
		t.Fatalf("added len = %d, want 6", len(added))
	}
}

func TestEnsureLiveKitWorkspaceFileToolsAllowsExecFromLiveKitChannel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = false
	cfg.Tools.Exec.AllowRemote = false

	instance := &agent.AgentInstance{
		Workspace: t.TempDir(),
		Tools:     tools.NewToolRegistry(),
	}

	ensureLiveKitWorkspaceFileTools(instance, &cfg.Agents.Defaults, cfg)

	execTool, ok := instance.Tools.Get("exec")
	if !ok {
		t.Fatal("exec should be registered for LiveKit active-skill agents")
	}
	result := execTool.Execute(tools.WithToolContext(context.Background(), "livekit", "session-a"), map[string]any{
		"action":  "run",
		"command": "echo livekit-ok",
	})
	if result.IsError {
		t.Fatalf("exec should be allowed from livekit channel, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "livekit-ok") {
		t.Fatalf("exec output missing marker, got: %s", result.ForLLM)
	}
}

func TestEnsureLiveKitWorkspaceFileToolsReplacesDefaultWorkspaceTools(t *testing.T) {
	cfg := config.DefaultConfig()
	defaults := cfg.Agents.Defaults

	defaultWorkspace := t.TempDir()
	deviceWorkspace := t.TempDir()
	defaults.Workspace = defaultWorkspace

	instance := &agent.AgentInstance{
		Workspace: deviceWorkspace,
		Tools:     tools.NewToolRegistry(),
	}
	instance.Tools.Register(tools.NewWriteFileTool(defaultWorkspace, true))
	instance.Tools.Register(tools.NewListDirTool(defaultWorkspace, true))
	instance.Tools.Register(tools.NewReadFileTool(defaultWorkspace, true, cfg.Tools.ReadFile.MaxReadFileSize))

	ensureLiveKitWorkspaceFileTools(instance, &defaults, cfg)

	writeTool, ok := instance.Tools.Get("write_file")
	if !ok {
		t.Fatal("write_file should be registered")
	}
	result := writeTool.Execute(context.Background(), map[string]any{
		"path":      filepath.Join(deviceWorkspace, "flower_song.txt"),
		"content":   "petals",
		"overwrite": true,
	})
	if result.IsError {
		t.Fatalf("write_file should allow absolute path inside device workspace, got: %s", result.ForLLM)
	}

	if _, err := os.Stat(filepath.Join(deviceWorkspace, "flower_song.txt")); err != nil {
		t.Fatalf("expected file in device workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultWorkspace, "flower_song.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected no file in default workspace, got err=%v", err)
	}
}
