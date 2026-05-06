package main

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// scopedMCPConfigForWorkspace returns a per-session config copy when filesystem MCP
// needs workspace-specific allowed directories. It leaves the original config untouched.
func scopedMCPConfigForWorkspace(base *config.Config, workspace string) *config.Config {
	workspace = strings.TrimSpace(workspace)
	if base == nil || workspace == "" {
		return base
	}
	serverCfg, ok := base.Tools.MCP.Servers["filesystem"]
	if !ok || !serverCfg.Enabled {
		return base
	}

	updated, changed := rewriteFilesystemMCPServerArgs(serverCfg, workspace, base.WorkspacePath())
	if !changed {
		return base
	}

	clone := *base
	clone.Tools = base.Tools
	clone.Tools.MCP = base.Tools.MCP
	clone.Tools.MCP.Servers = make(map[string]config.MCPServerConfig, len(base.Tools.MCP.Servers))
	for name, cfg := range base.Tools.MCP.Servers {
		clone.Tools.MCP.Servers[name] = cfg
	}
	clone.Tools.MCP.Servers["filesystem"] = updated
	return &clone
}

func rewriteFilesystemMCPServerArgs(
	server config.MCPServerConfig,
	workspace string,
	defaultWorkspace string,
) (config.MCPServerConfig, bool) {
	args := append([]string(nil), server.Args...)
	workspace = strings.TrimSpace(workspace)
	defaultWorkspace = strings.TrimSpace(defaultWorkspace)
	if workspace == "" {
		return server, false
	}
	changed := false
	placeholderReplaced := false

	for i, arg := range args {
		trimmed := strings.TrimSpace(arg)
		lower := strings.ToLower(trimmed)
		switch lower {
		case "$workspace", "${workspace}", "{{workspace}}":
			args[i] = workspace
			placeholderReplaced = true
			changed = true
			continue
		}
		if trimmed == defaultWorkspace || trimmed == "/root/.picoclaw/workspace" {
			args[i] = workspace
			changed = true
		}
	}
	if placeholderReplaced {
		server.Args = args
		return server, changed
	}

	// If this is the official filesystem server invocation, force allowed dirs to this workspace.
	serverPkgIdx := -1
	for i, arg := range args {
		if strings.Contains(arg, "server-filesystem") {
			serverPkgIdx = i
			break
		}
	}
	if serverPkgIdx >= 0 {
		allowedStart := serverPkgIdx + 1
		if allowedStart >= len(args) {
			server.Args = append(args, workspace)
			return server, true
		}
		desired := append(append([]string{}, args[:allowedStart]...), workspace)
		if len(desired) != len(args) {
			changed = true
		} else {
			for i := range desired {
				if desired[i] != args[i] {
					changed = true
					break
				}
			}
		}
		server.Args = desired
		return server, changed
	}

	if len(args) == 0 {
		server.Args = []string{workspace}
		return server, true
	}
	server.Args = args
	return server, changed
}
