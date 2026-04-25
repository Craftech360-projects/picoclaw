// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type mcpRuntime struct {
	initOnce sync.Once
	mu       sync.Mutex
	manager  *mcp.Manager
	initErr  error
}

type mcpToolManager interface {
	tools.MCPManager
	GetServers() map[string]*mcp.ServerConnection
	Close() error
}

func (r *mcpRuntime) setManager(manager *mcp.Manager) {
	r.mu.Lock()
	r.manager = manager
	r.initErr = nil
	r.mu.Unlock()
}

func (r *mcpRuntime) setInitErr(err error) {
	r.mu.Lock()
	r.initErr = err
	r.mu.Unlock()
}

func (r *mcpRuntime) getInitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initErr
}

func (r *mcpRuntime) takeManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	manager := r.manager
	r.manager = nil
	return manager
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager != nil
}

// ensureMCPInitialized loads MCP servers/tools once so both Run() and direct
// agent mode share the same initialization path.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	al.mcp.initOnce.Do(func() {
		defaultAgent := al.registry.GetDefaultAgent()
		workspacePath := al.cfg.WorkspacePath()
		if defaultAgent != nil && defaultAgent.Workspace != "" {
			workspacePath = defaultAgent.Workspace
		}

		var instances []*AgentInstance
		for _, agentID := range al.registry.ListAgentIDs() {
			if agent, ok := al.registry.GetAgent(agentID); ok {
				instances = append(instances, agent)
			}
		}

		mcpManager, err := RegisterMCPToolsForInstances(ctx, al.cfg, workspacePath, instances...)
		if err != nil {
			al.mcp.setInitErr(err)
			return
		}
		if mcpManager != nil {
			al.mcp.setManager(mcpManager)
		}
	})

	return al.mcp.getInitErr()
}

// RegisterMCPToolsForInstances loads configured MCP servers and registers their
// tools onto the provided agent instances. It returns the live manager when MCP
// was initialized so callers that own a shorter runtime can close it later.
func RegisterMCPToolsForInstances(
	ctx context.Context,
	cfg *config.Config,
	workspacePath string,
	instances ...*AgentInstance,
) (*mcp.Manager, error) {
	if cfg == nil || !cfg.Tools.IsToolEnabled("mcp") {
		return nil, nil
	}

	if len(cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return nil, nil
	}

	if !hasEnabledMCPServer(cfg.Tools.MCP.Servers) {
		logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
		return nil, nil
	}

	mcpManager := mcp.NewManager()
	if err := mcpManager.LoadFromMCPConfig(ctx, cfg.Tools.MCP, workspacePath); err != nil {
		logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
			map[string]any{
				"error": err.Error(),
			})
		closeMCPManager(mcpManager)
		return nil, nil
	}

	if err := registerMCPToolsFromManager(mcpManager, cfg, instances); err != nil {
		closeMCPManager(mcpManager)
		return nil, err
	}

	return mcpManager, nil
}

func registerMCPToolsFromManager(
	mcpManager mcpToolManager,
	cfg *config.Config,
	instances []*AgentInstance,
) error {
	if mcpManager == nil || cfg == nil {
		return nil
	}

	agents := liveAgentInstances(instances)
	servers := mcpManager.GetServers()
	uniqueTools := 0
	totalRegistrations := 0

	for serverName, conn := range servers {
		if conn == nil {
			continue
		}
		uniqueTools += len(conn.Tools)
		serverCfg := cfg.Tools.MCP.Servers[serverName]
		registerAsHidden := serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg)

		for _, tool := range conn.Tools {
			if tool == nil {
				continue
			}
			for _, agent := range agents {
				mcpTool := tools.NewMCPTool(mcpManager, serverName, tool)

				if registerAsHidden {
					agent.Tools.RegisterHidden(mcpTool)
				} else {
					agent.Tools.Register(mcpTool)
				}

				totalRegistrations++
				logger.DebugCF("agent", "Registered MCP tool",
					map[string]any{
						"agent_id": agent.ID,
						"server":   serverName,
						"tool":     tool.Name,
						"name":     mcpTool.Name(),
						"deferred": registerAsHidden,
					})
			}
		}
	}

	logger.InfoCF("agent", "MCP tools registered successfully",
		map[string]any{
			"server_count":        len(servers),
			"unique_tools":        uniqueTools,
			"total_registrations": totalRegistrations,
			"agent_count":         len(agents),
		})

	if cfg.Tools.MCP.Enabled && cfg.Tools.MCP.Discovery.Enabled {
		if err := registerMCPDiscoveryTools(cfg, agents); err != nil {
			return err
		}
	}

	return nil
}

func registerMCPDiscoveryTools(cfg *config.Config, agents []*AgentInstance) error {
	useBM25 := cfg.Tools.MCP.Discovery.UseBM25
	useRegex := cfg.Tools.MCP.Discovery.UseRegex

	if !useBM25 && !useRegex {
		return fmt.Errorf(
			"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
		)
	}

	ttl := cfg.Tools.MCP.Discovery.TTL
	if ttl <= 0 {
		ttl = 5
	}

	maxSearchResults := cfg.Tools.MCP.Discovery.MaxSearchResults
	if maxSearchResults <= 0 {
		maxSearchResults = 5
	}

	logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
		"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
	})

	for _, agent := range agents {
		if useRegex {
			agent.Tools.Register(tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
		}
		if useBM25 {
			agent.Tools.Register(tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
		}
	}

	return nil
}

func liveAgentInstances(instances []*AgentInstance) []*AgentInstance {
	agents := make([]*AgentInstance, 0, len(instances))
	for _, agent := range instances {
		if agent == nil || agent.Tools == nil {
			continue
		}
		agents = append(agents, agent)
	}
	return agents
}

func hasEnabledMCPServer(servers map[string]config.MCPServerConfig) bool {
	for _, serverCfg := range servers {
		if serverCfg.Enabled {
			return true
		}
	}
	return false
}

func closeMCPManager(manager interface{ Close() error }) {
	if manager == nil {
		return
	}
	if closeErr := manager.Close(); closeErr != nil {
		logger.ErrorCF("agent", "Failed to close MCP manager",
			map[string]any{
				"error": closeErr.Error(),
			})
	}
}

// serverIsDeferred reports whether an MCP server's tools should be registered
// as hidden (deferred/discovery mode).
//
// The per-server Deferred field takes precedence over the global discoveryEnabled
// default. When Deferred is nil, discoveryEnabled is used as the fallback.
func serverIsDeferred(discoveryEnabled bool, serverCfg config.MCPServerConfig) bool {
	if !discoveryEnabled {
		return false
	}
	if serverCfg.Deferred != nil {
		return *serverCfg.Deferred
	}
	return true
}
