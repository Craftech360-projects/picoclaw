package main

import (
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRewriteFilesystemMCPServerArgs_ReplacesDefaultWorkspaceArg(t *testing.T) {
	server := config.MCPServerConfig{
		Enabled: true,
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace"},
	}
	updated, changed := rewriteFilesystemMCPServerArgs(server, "/root/.picoclaw/workspace-device-abc", "/root/.picoclaw/workspace")
	if !changed {
		t.Fatal("expected args change")
	}
	want := []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace-device-abc"}
	if !reflect.DeepEqual(updated.Args, want) {
		t.Fatalf("args = %#v, want %#v", updated.Args, want)
	}
}

func TestRewriteFilesystemMCPServerArgs_UsesSingleAllowedDirForFilesystemServer(t *testing.T) {
	server := config.MCPServerConfig{
		Enabled: true,
		Command: "npx",
		Args: []string{
			"-y",
			"@modelcontextprotocol/server-filesystem",
			"/tmp/old1",
			"/tmp/old2",
		},
	}
	updated, changed := rewriteFilesystemMCPServerArgs(server, "/root/.picoclaw/workspace-device-xyz", "/root/.picoclaw/workspace")
	if !changed {
		t.Fatal("expected args change")
	}
	want := []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace-device-xyz"}
	if !reflect.DeepEqual(updated.Args, want) {
		t.Fatalf("args = %#v, want %#v", updated.Args, want)
	}
}

func TestScopedMCPConfigForWorkspace_DoesNotMutateBaseConfig(t *testing.T) {
	base := config.DefaultConfig()
	base.Tools.MCP.Enabled = true
	base.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"filesystem": {
			Enabled: true,
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace"},
		},
	}
	base.Agents.Defaults.Workspace = "/root/.picoclaw/workspace"

	scoped := scopedMCPConfigForWorkspace(base, "/root/.picoclaw/workspace-device-28562f0787a8")
	if scoped == base {
		t.Fatal("expected cloned config pointer")
	}
	gotScoped := scoped.Tools.MCP.Servers["filesystem"].Args
	wantScoped := []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace-device-28562f0787a8"}
	if !reflect.DeepEqual(gotScoped, wantScoped) {
		t.Fatalf("scoped filesystem args = %#v, want %#v", gotScoped, wantScoped)
	}
	gotBase := base.Tools.MCP.Servers["filesystem"].Args
	wantBase := []string{"-y", "@modelcontextprotocol/server-filesystem", "/root/.picoclaw/workspace"}
	if !reflect.DeepEqual(gotBase, wantBase) {
		t.Fatalf("base filesystem args mutated = %#v, want %#v", gotBase, wantBase)
	}
}
