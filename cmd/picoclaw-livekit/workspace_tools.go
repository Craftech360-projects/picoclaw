package main

import (
	"sort"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

var liveKitRequiredWorkspaceFileTools = []string{"read_file", "write_file", "list_dir", "exec", "web_fetch", "web_search"}

func ensureLiveKitWorkspaceFileTools(
	agentInstance *agent.AgentInstance,
	defaults *config.AgentDefaults,
	cfg *config.Config,
) []string {
	if agentInstance == nil || agentInstance.Tools == nil {
		return nil
	}

	missingBefore := make(map[string]struct{}, len(liveKitRequiredWorkspaceFileTools))
	for _, name := range liveKitRequiredWorkspaceFileTools {
		if _, ok := agentInstance.Tools.Get(name); !ok {
			missingBefore[name] = struct{}{}
		}
	}

	agent.RegisterWorkspaceTools(
		agentInstance.Tools,
		agentInstance.Workspace,
		defaults,
		cfg,
		agent.WorkspaceToolRegistrationOptions{
			ForceFileTools:   true,
			ReplaceFileTools: true,
		},
	)
	registerLiveKitRuntimeTools(agentInstance, cfg)

	added := make([]string, 0, len(missingBefore))
	for name := range missingBefore {
		if _, ok := agentInstance.Tools.Get(name); ok {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return added
}

func registerLiveKitRuntimeTools(agentInstance *agent.AgentInstance, cfg *config.Config) {
	if agentInstance == nil || agentInstance.Tools == nil || cfg == nil {
		return
	}

	if _, ok := agentInstance.Tools.Get("exec"); !ok {
		execCfg := *cfg
		execCfg.Tools.Exec.Enabled = true
		execCfg.Tools.Exec.AllowRemote = true
		execCfg.Tools.Exec.EnableDenyPatterns = true
		if execCfg.Tools.Exec.TimeoutSeconds <= 0 {
			execCfg.Tools.Exec.TimeoutSeconds = 30
		}
		execTool, err := tools.NewExecToolWithConfig(agentInstance.Workspace, true, &execCfg)
		if err != nil {
			logger.WarnCF("livekit", "Failed to force required exec tool for LiveKit agent", map[string]any{
				"error": err.Error(),
			})
		} else {
			agentInstance.Tools.Register(execTool)
		}
	}

	if _, ok := agentInstance.Tools.Get("web_fetch"); !ok {
		fetchTool, err := tools.NewWebFetchToolWithProxy(
			50000,
			cfg.Tools.Web.Proxy,
			cfg.Tools.Web.Format,
			cfg.Tools.Web.FetchLimitBytes,
			cfg.Tools.Web.PrivateHostWhitelist,
		)
		if err != nil {
			logger.WarnCF("livekit", "Failed to force required web_fetch tool for LiveKit agent", map[string]any{
				"error": err.Error(),
			})
		} else {
			agentInstance.Tools.Register(fetchTool)
		}
	}

	if _, ok := agentInstance.Tools.Get("web_search"); !ok {
		searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{
			BraveAPIKeys:          config.MergeAPIKeys(cfg.Tools.Web.Brave.APIKey(), cfg.Tools.Web.Brave.APIKeys()),
			BraveMaxResults:       cfg.Tools.Web.Brave.MaxResults,
			BraveEnabled:          cfg.Tools.Web.Brave.Enabled,
			TavilyAPIKeys:         config.MergeAPIKeys(cfg.Tools.Web.Tavily.APIKey(), cfg.Tools.Web.Tavily.APIKeys()),
			TavilyBaseURL:         cfg.Tools.Web.Tavily.BaseURL,
			TavilyMaxResults:      cfg.Tools.Web.Tavily.MaxResults,
			TavilyEnabled:         cfg.Tools.Web.Tavily.Enabled,
			DuckDuckGoMaxResults:  cfg.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:     true,
			PerplexityAPIKeys:     config.MergeAPIKeys(cfg.Tools.Web.Perplexity.APIKey(), cfg.Tools.Web.Perplexity.APIKeys()),
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
			logger.WarnCF("livekit", "Failed to force required web_search tool for LiveKit agent", map[string]any{
				"error": err.Error(),
			})
		} else if searchTool != nil {
			agentInstance.Tools.Register(searchTool)
		}
	}
}
