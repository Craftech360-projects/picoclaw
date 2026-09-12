package gptlive

import "github.com/sipeed/picoclaw/pkg/providers"

// FunctionTools converts picoclaw tool definitions (chat-completions shape, nested
// under "function") to the flat shape the Responses backend expects.
func FunctionTools(defs []providers.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		if d.Function.Name == "" {
			continue
		}
		params := d.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        d.Function.Name,
			"description": d.Function.Description,
			"parameters":  params,
		})
	}
	return out
}

// HostedWebSearch is OpenAI's server-side web search, run by the backend model.
func HostedWebSearch() map[string]any {
	return map[string]any{"type": "web_search"}
}
