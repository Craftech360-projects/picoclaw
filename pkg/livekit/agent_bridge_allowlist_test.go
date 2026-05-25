package livekit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestExecuteToolBlocksNonAllowlistedTool(t *testing.T) {
	workspace := t.TempDir()
	registry := tools.NewToolRegistry()
	registry.Register(tools.NewWriteFileTool(workspace, true))

	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			Workspace: workspace,
			Tools:     registry,
		},
		tools:            registry,
		allowedToolNames: map[string]struct{}{"read_file": {}},
	}

	result := bridge.executeTool(context.Background(), "session-1", providers.ToolCall{
		Name: "write_file",
		Arguments: map[string]any{
			"path":      filepath.Join(workspace, "notes.txt"),
			"content":   "hello",
			"overwrite": true,
		},
	})
	if result == nil || !result.IsError {
		t.Fatalf("result = %#v, want blocked error", result)
	}
	if !strings.Contains(strings.ToLower(result.ForLLM), "not allowed") {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
}
