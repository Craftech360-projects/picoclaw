package agent

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// SharedToolDependencies contains the dependencies needed to register shared tools.
type SharedToolDependencies struct {
	Config         *config.Config
	MessageBus     *bus.MessageBus
	Registry       *AgentRegistry
	Provider       providers.LLMProvider
	SubTurnSpawner tools.SubTurnSpawner

	// SubagentSpawner replaces the hardcoded spawnSubTurn from AgentLoop.
	// This function handles the actual execution of a subagent when requested by a tool.
	SubagentSpawner func(
		ctx context.Context,
		task, label, targetAgentID string,
		tls *tools.ToolRegistry,
		maxTokens int,
		temperature float64,
		hasMaxTokens, hasTemperature bool,
		agent *AgentInstance,
	) (*tools.ToolResult, error)
}

// RegisterSharedTools registers tools that are shared across all configured agents.
// It uses dependency injection so that it can be reused by AgentBridge in addition to AgentLoop.
func RegisterSharedTools(deps SharedToolDependencies) {
	cfg := deps.Config
	registry := deps.Registry
	msgBus := deps.MessageBus
	provider := deps.Provider

	allowReadPaths := buildAllowReadPatterns(cfg)

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		if cfg.Tools.IsToolEnabled("web") {
			searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{
				BraveAPIKeys:    config.MergeAPIKeys(cfg.Tools.Web.Brave.APIKey(), cfg.Tools.Web.Brave.APIKeys()),
				BraveMaxResults: cfg.Tools.Web.Brave.MaxResults,
				BraveEnabled:    cfg.Tools.Web.Brave.Enabled,
				TavilyAPIKeys: config.MergeAPIKeys(
					cfg.Tools.Web.Tavily.APIKey(),
					cfg.Tools.Web.Tavily.APIKeys(),
				),
				TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
				TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
				TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
				DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
				DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
				PerplexityAPIKeys: config.MergeAPIKeys(
					cfg.Tools.Web.Perplexity.APIKey(),
					cfg.Tools.Web.Perplexity.APIKeys(),
				),
				PerplexityMaxResults:  cfg.Tools.Web.Perplexity.MaxResults,
				PerplexityEnabled:     cfg.Tools.Web.Perplexity.Enabled,
				SearXNGBaseURL:        cfg.Tools.Web.SearXNG.BaseURL,
				SearXNGMaxResults:     cfg.Tools.Web.SearXNG.MaxResults,
				SearXNGEnabled:        cfg.Tools.Web.SearXNG.Enabled,
				GLMSearchAPIKey:       cfg.Tools.Web.GLMSearch.APIKey(),
				GLMSearchBaseURL:      cfg.Tools.Web.GLMSearch.BaseURL,
				GLMSearchEngine:       cfg.Tools.Web.GLMSearch.SearchEngine,
				GLMSearchMaxResults:   cfg.Tools.Web.GLMSearch.MaxResults,
				GLMSearchEnabled:      cfg.Tools.Web.GLMSearch.Enabled,
				BaiduSearchAPIKey:     cfg.Tools.Web.BaiduSearch.APIKey(),
				BaiduSearchBaseURL:    cfg.Tools.Web.BaiduSearch.BaseURL,
				BaiduSearchMaxResults: cfg.Tools.Web.BaiduSearch.MaxResults,
				BaiduSearchEnabled:    cfg.Tools.Web.BaiduSearch.Enabled,
				Proxy:                 cfg.Tools.Web.Proxy,
			})
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"error": err.Error()})
			} else if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(
				50000,
				cfg.Tools.Web.Proxy,
				cfg.Tools.Web.Format,
				cfg.Tools.Web.FetchLimitBytes,
				cfg.Tools.Web.PrivateHostWhitelist)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			messageTool.SetSendCallback(func(channel, chatID, content string) error {
				if msgBus == nil {
					return nil // graceful fallback if used in AgentBridge
				}
				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				return msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
					Channel: channel,
					ChatID:  chatID,
					Content: content,
				})
			})
			agent.Tools.Register(messageTool)
		}

		// Send file tool
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(sendFileTool)
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			clawHubConfig := cfg.Tools.Skills.Registries.ClawHub
			registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
				MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
				ClawHub: skills.ClawHubConfig{
					Enabled:         clawHubConfig.Enabled,
					BaseURL:         clawHubConfig.BaseURL,
					AuthToken:       clawHubConfig.AuthToken(),
					SearchPath:      clawHubConfig.SearchPath,
					SkillsPath:      clawHubConfig.SkillsPath,
					DownloadPath:    clawHubConfig.DownloadPath,
					Timeout:         clawHubConfig.Timeout,
					MaxZipSize:      clawHubConfig.MaxZipSize,
					MaxResponseSize: clawHubConfig.MaxResponseSize,
				},
			})

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// Subagents / Spawn tools
		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		spawnStatusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
		if (spawnEnabled || spawnStatusEnabled) && cfg.Tools.IsToolEnabled("subagent") {
			subagentManager := tools.NewSubagentManager(provider, agent.Model, agent.Workspace)
			subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)

			if deps.SubagentSpawner != nil {
				subagentManager.SetSpawner(func(
					ctx context.Context,
					task, label, targetAgentID string,
					tls *tools.ToolRegistry,
					maxTokens int,
					temperature float64,
					hasMaxTokens, hasTemperature bool,
				) (*tools.ToolResult, error) {
					return deps.SubagentSpawner(ctx, task, label, targetAgentID, tls, maxTokens, temperature, hasMaxTokens, hasTemperature, agent)
				})
			}

			subagentManager.SetTools(agent.Tools.Clone())
			if spawnEnabled {
				spawnTool := tools.NewSpawnTool(subagentManager)
				if deps.SubTurnSpawner != nil {
					spawnTool.SetSpawner(deps.SubTurnSpawner)
				}
				currentAgentID := agentID
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})

				agent.Tools.Register(spawnTool)

				subagentTool := tools.NewSubagentTool(subagentManager)
				if deps.SubTurnSpawner != nil {
					subagentTool.SetSpawner(deps.SubTurnSpawner)
				}
				agent.Tools.Register(subagentTool)
			}
			if spawnStatusEnabled {
				agent.Tools.Register(tools.NewSpawnStatusTool(subagentManager))
			}
		}
	}
}
