package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

var liveKitRequiredWorkspaceFileTools = []string{
	"read_file",
	"write_file",
	"list_dir",
	"web_fetch",
	"web_search",
	"get_weather",
	"get_time_date",
}
var liveKitVoiceAllowedTools = []string{
	"read_file",
	"write_file",
	"list_dir",
	"web_fetch",
	"web_search",
	"get_weather",
	"get_time_date",
}

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

	cfgForWorkspace := *cfg
	cfgForWorkspace.Tools.Exec.Enabled = false
	agent.RegisterWorkspaceTools(
		agentInstance.Tools,
		agentInstance.Workspace,
		defaults,
		&cfgForWorkspace,
		agent.WorkspaceToolRegistrationOptions{
			ForceFileTools:   true,
			ReplaceFileTools: true,
		},
	)
	registerLiveKitRuntimeTools(agentInstance, cfg)
	enforceLiveKitWritePathGuard(agentInstance)

	added := make([]string, 0, len(missingBefore))
	for name := range missingBefore {
		if _, ok := agentInstance.Tools.Get(name); ok {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return added
}

type liveKitWriteGuardTool struct {
	delegate  tools.Tool
	workspace string
}

var liveKitWriteAllowedRelativePaths = map[string]struct{}{
	"user.md":          {},
	"memory/memory.md": {},
}

func newLiveKitWriteGuardTool(delegate tools.Tool, workspace string) tools.Tool {
	if delegate == nil {
		return nil
	}
	return &liveKitWriteGuardTool{
		delegate:  delegate,
		workspace: strings.TrimSpace(workspace),
	}
}

func (t *liveKitWriteGuardTool) Name() string {
	return t.delegate.Name()
}

func (t *liveKitWriteGuardTool) Description() string {
	return t.delegate.Description()
}

func (t *liveKitWriteGuardTool) Parameters() map[string]any {
	params := cloneToolParameters(t.delegate.Parameters())
	required, _ := params["required"].([]string)
	if len(required) == 0 {
		return params
	}
	filtered := make([]string, 0, len(required))
	for _, item := range required {
		if strings.EqualFold(strings.TrimSpace(item), "path") {
			continue
		}
		filtered = append(filtered, item)
	}
	params["required"] = filtered
	return params
}

func (t *liveKitWriteGuardTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	rawPath, _ := args["path"].(string)
	args = normalizeLiveKitWriteArgs(args, t.workspace)
	rawPath, _ = args["path"].(string)
	if !isLiveKitAllowedWritePath(rawPath, t.workspace) {
		return tools.ErrorResult("write_file blocked in voice runtime: only USER.md and memory/MEMORY.md are writable")
	}
	return t.delegate.Execute(ctx, args)
}

func normalizeLiveKitWriteArgs(args map[string]any, workspace string) map[string]any {
	if args == nil {
		return args
	}
	rawPath, _ := args["path"].(string)
	if isLiveKitAllowedWritePath(rawPath, workspace) {
		return args
	}
	if strings.TrimSpace(rawPath) != "" && !looksLikeWorkspaceRootPath(rawPath, workspace) {
		return args
	}

	content, _ := args["content"].(string)
	target := inferLiveKitWritableProfilePath(content)
	if target == "" {
		return args
	}

	normalized := make(map[string]any, len(args))
	for key, value := range args {
		normalized[key] = value
	}
	normalized["path"] = target
	return normalized
}

func cloneToolParameters(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	out := make(map[string]any, len(params))
	for key, value := range params {
		switch typed := value.(type) {
		case []string:
			out[key] = append([]string(nil), typed...)
		case []any:
			out[key] = append([]any(nil), typed...)
		case map[string]any:
			nested := make(map[string]any, len(typed))
			for nestedKey, nestedValue := range typed {
				nested[nestedKey] = nestedValue
			}
			out[key] = nested
		default:
			out[key] = value
		}
	}
	return out
}

func inferLiveKitWritableProfilePath(content string) string {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "# user") ||
		strings.Contains(lower, "## personal information") ||
		strings.Contains(lower, "## preferences"):
		return "USER.md"
	case strings.HasPrefix(lower, "# memory") ||
		strings.HasPrefix(lower, "# long-term memory"):
		return "memory/MEMORY.md"
	default:
		return ""
	}
}

func looksLikeWorkspaceRootPath(rawPath, workspace string) bool {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return false
	}
	cleaned := filepath.Clean(path)
	if cleaned == "/root" || cleaned == "/root/." {
		return true
	}
	base := strings.ToLower(filepath.Base(cleaned))
	if base == "workspace-" || strings.HasPrefix(base, "workspace-device-") {
		return true
	}
	if workspace != "" {
		absWorkspace, err := filepath.Abs(workspace)
		if err == nil {
			absPath, err := filepath.Abs(path)
			if err == nil && absPath == absWorkspace {
				return true
			}
		}
	}
	return false
}

func enforceLiveKitWritePathGuard(agentInstance *agent.AgentInstance) {
	if agentInstance == nil || agentInstance.Tools == nil {
		return
	}
	writeTool, ok := agentInstance.Tools.Get("write_file")
	if !ok || writeTool == nil {
		return
	}
	if _, alreadyWrapped := writeTool.(*liveKitWriteGuardTool); alreadyWrapped {
		return
	}
	guarded := newLiveKitWriteGuardTool(writeTool, agentInstance.Workspace)
	if guarded != nil {
		agentInstance.Tools.Register(guarded)
	}
}

func isLiveKitAllowedWritePath(rawPath, workspace string) bool {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return false
	}

	normalizedRel := normalizeLiveKitRelativePath(path)
	if normalizedRel != "" {
		if _, ok := liveKitWriteAllowedRelativePaths[normalizedRel]; ok {
			return true
		}
	}

	if workspace == "" || !filepath.IsAbs(path) {
		return false
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil {
		return false
	}
	rel = normalizeLiveKitRelativePath(rel)
	if rel == "" || strings.HasPrefix(rel, "../") || rel == ".." {
		return false
	}
	_, ok := liveKitWriteAllowedRelativePaths[rel]
	return ok
}

func normalizeLiveKitRelativePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	normalized := filepath.ToSlash(cleaned)
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/")
	normalized = strings.ToLower(normalized)
	return normalized
}

func registerLiveKitRuntimeTools(agentInstance *agent.AgentInstance, cfg *config.Config) {
	if agentInstance == nil || agentInstance.Tools == nil || cfg == nil {
		return
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

	if _, ok := agentInstance.Tools.Get("get_weather"); !ok {
		agentInstance.Tools.Register(tools.NewGetWeatherTool())
	}

	if _, ok := agentInstance.Tools.Get("get_time_date"); !ok {
		agentInstance.Tools.Register(tools.NewGetTimeDateTool())
	}
}

func liveKitVoiceToolAllowlist() []string {
	out := make([]string, len(liveKitVoiceAllowedTools))
	copy(out, liveKitVoiceAllowedTools)
	return out
}

func isLiveKitVoiceAllowedTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, allowed := range liveKitVoiceAllowedTools {
		if strings.EqualFold(allowed, name) {
			return true
		}
	}
	return false
}
