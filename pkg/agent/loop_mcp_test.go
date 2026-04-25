// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func boolPtr(b bool) *bool { return &b }

type fakeMCPToolManager struct {
	servers map[string]*picomcp.ServerConnection
	closed  bool
}

func (f *fakeMCPToolManager) GetServers() map[string]*picomcp.ServerConnection {
	return f.servers
}

func (f *fakeMCPToolManager) CallTool(
	context.Context,
	string,
	string,
	map[string]any,
) (*sdkmcp.CallToolResult, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
	}, nil
}

func (f *fakeMCPToolManager) Close() error {
	f.closed = true
	return nil
}

func newTestMCPManager() *fakeMCPToolManager {
	return &fakeMCPToolManager{
		servers: map[string]*picomcp.ServerConnection{
			"github": {
				Name: "github",
				Tools: []*sdkmcp.Tool{
					{
						Name:        "create_issue",
						Description: "Create a GitHub issue",
						InputSchema: map[string]any{
							"type":       "object",
							"properties": map[string]any{},
						},
					},
				},
			},
		},
	}
}

func TestRegisterMCPToolsFromManagerRegistersVisibleTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Discovery.Enabled = false
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}
	instance := &AgentInstance{
		ID:        "main",
		Workspace: t.TempDir(),
		Tools:     tools.NewToolRegistry(),
	}

	if err := registerMCPToolsFromManager(newTestMCPManager(), cfg, []*AgentInstance{instance}); err != nil {
		t.Fatalf("registerMCPToolsFromManager() error = %v", err)
	}

	tool, ok := instance.Tools.Get("mcp_github_create_issue")
	if !ok {
		t.Fatal("expected visible MCP tool to be registered")
	}
	if !strings.Contains(tool.Description(), "[MCP:github]") {
		t.Fatalf("expected MCP description prefix, got %q", tool.Description())
	}
}

func TestRegisterMCPToolsFromManagerRegistersDeferredDiscoveryTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Discovery.Enabled = true
	cfg.Tools.MCP.Discovery.UseBM25 = true
	cfg.Tools.MCP.Discovery.UseRegex = false
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}
	instance := &AgentInstance{
		ID:        "main",
		Workspace: t.TempDir(),
		Tools:     tools.NewToolRegistry(),
	}

	if err := registerMCPToolsFromManager(newTestMCPManager(), cfg, []*AgentInstance{instance}); err != nil {
		t.Fatalf("registerMCPToolsFromManager() error = %v", err)
	}

	if _, ok := instance.Tools.Get("mcp_github_create_issue"); ok {
		t.Fatal("deferred MCP tool should not be directly visible before discovery promotion")
	}
	if _, ok := instance.Tools.Get("tool_search_tool_bm25"); !ok {
		t.Fatal("expected BM25 discovery tool to be registered")
	}
	hidden := instance.Tools.SnapshotHiddenTools()
	if len(hidden.Docs) != 1 || hidden.Docs[0].Name != "mcp_github_create_issue" {
		t.Fatalf("expected deferred MCP tool in hidden snapshot, got %#v", hidden.Docs)
	}
}

func TestRegisterMCPToolsFromManagerRejectsDiscoveryWithoutSearchMethod(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Discovery.Enabled = true
	cfg.Tools.MCP.Discovery.UseBM25 = false
	cfg.Tools.MCP.Discovery.UseRegex = false
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Enabled: true},
	}
	instance := &AgentInstance{
		ID:        "main",
		Workspace: t.TempDir(),
		Tools:     tools.NewToolRegistry(),
	}

	err := registerMCPToolsFromManager(newTestMCPManager(), cfg, []*AgentInstance{instance})
	if err == nil || !strings.Contains(err.Error(), "tool discovery is enabled") {
		t.Fatalf("expected invalid discovery config error, got %v", err)
	}
}

func TestServerIsDeferred(t *testing.T) {
	tests := []struct {
		name             string
		discoveryEnabled bool
		serverDeferred   *bool
		want             bool
	}{
		// --- global false always wins: per-server deferred is ignored ---
		{
			name:             "global false: per-server deferred=true is ignored",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(true),
			want:             false,
		},
		{
			name:             "global false: per-server deferred=false stays false",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		// --- global true: per-server override applies ---
		{
			name:             "global true: per-server deferred=false opts out",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		{
			name:             "global true: per-server deferred=true stays true",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(true),
			want:             true,
		},
		// --- no per-server override: fall back to global ---
		{
			name:             "no per-server field, global discovery enabled",
			discoveryEnabled: true,
			serverDeferred:   nil,
			want:             true,
		},
		{
			name:             "no per-server field, global discovery disabled",
			discoveryEnabled: false,
			serverDeferred:   nil,
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverCfg := config.MCPServerConfig{Deferred: tt.serverDeferred}
			got := serverIsDeferred(tt.discoveryEnabled, serverCfg)
			if got != tt.want {
				t.Errorf("serverIsDeferred(discoveryEnabled=%v, deferred=%v) = %v, want %v",
					tt.discoveryEnabled, tt.serverDeferred, got, tt.want)
			}
		})
	}
}
