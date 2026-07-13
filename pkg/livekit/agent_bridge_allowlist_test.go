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

func TestDefaultVoiceToolAllowlist_AppliedWhenConfigEmpty(t *testing.T) {
	got := defaultVoiceToolAllowlist()
	want := []string{
		"get_weather", "get_time_date", "web_search", "web_fetch",
		"read_file", "write_file", "list_dir",
	}
	if len(got) != len(want) {
		t.Fatalf("defaultVoiceToolAllowlist len=%d, want %d (%v)", len(got), len(want), got)
	}
	set := normalizeAllowedToolNames(got)
	for _, name := range want {
		if _, ok := set[name]; !ok {
			t.Errorf("default allowlist missing %q", name)
		}
	}
}

func TestFilterToolDefs_UsesDefaultWhenUnset(t *testing.T) {
	ab := &AgentBridge{allowedToolNames: normalizeAllowedToolNames(defaultVoiceToolAllowlist())}
	defs := []providers.ToolDefinition{
		{Function: providers.ToolFunctionDefinition{Name: "web_search"}},
		{Function: providers.ToolFunctionDefinition{Name: "spawn_subagent"}}, // not voice-usable
	}
	filtered := ab.filterToolDefsByAllowlist(defs)
	if len(filtered) != 1 || filtered[0].Function.Name != "web_search" {
		t.Fatalf("expected only web_search kept, got %+v", filtered)
	}
}
