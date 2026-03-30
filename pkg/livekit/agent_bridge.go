package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentBridge provides a simplified agent execution path for voice conversations.
type AgentBridge struct {
	cfg            *config.Config
	provider       providers.LLMProvider
	streamProvider providers.StreamingProvider
	modelID        string
	sessions       session.SessionStore
	tools          *tools.ToolRegistry
	contextBuilder *agent.ContextBuilder
	maxIterations  int
	llmOptions     map[string]any
}

// AgentBridgeConfig defines shared resources for creating bridges.
type AgentBridgeConfig struct {
	Config         *config.Config
	Provider       providers.LLMProvider
	ModelID        string
	Sessions       session.SessionStore
	Tools          *tools.ToolRegistry
	ContextBuilder *agent.ContextBuilder
	MaxIterations  int
	LLMOptions     map[string]any
}

// NewAgentBridge creates a new AgentBridge.
func NewAgentBridge(cfg AgentBridgeConfig) (*AgentBridge, error) {
	if cfg.Provider == nil {
		return nil, errors.New("provider is nil")
	}
	ab := &AgentBridge{
		cfg:            cfg.Config,
		provider:       cfg.Provider,
		modelID:        cfg.ModelID,
		sessions:       cfg.Sessions,
		tools:          cfg.Tools,
		contextBuilder: cfg.ContextBuilder,
		maxIterations:  cfg.MaxIterations,
		llmOptions:     cfg.LLMOptions,
	}
	if sp, ok := cfg.Provider.(providers.StreamingProvider); ok {
		ab.streamProvider = sp
	}
	if ab.maxIterations <= 0 {
		ab.maxIterations = 10
	}
	return ab, nil
}

// ChatStream sends a user message through the LLM and streams the response.
// If a tool call is required, it returns a boolean indicating that an async tool execution is pending.
func (ab *AgentBridge) ChatStream(ctx context.Context, sessionKey string, text string, cb func(chunk string), onDone func()) (bool, error) {
	if ab == nil {
		return false, errors.New("agent bridge is nil")
	}
	if ab.provider == nil {
		return false, errors.New("provider is nil")
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	var history []providers.Message
	var summary string
	if ab.sessions != nil {
		history = ab.sessions.GetHistory(sessionKey)
		summary = ab.sessions.GetSummary(sessionKey)
	}

	messages := ab.buildMessages(history, summary, text, sessionKey)
	if ab.sessions != nil && strings.TrimSpace(text) != "" {
		ab.sessions.AddMessage(sessionKey, "user", text)
	}

	return ab.runIteration(ctx, sessionKey, messages, cb, onDone)
}

func (ab *AgentBridge) runIteration(ctx context.Context, sessionKey string, messages []providers.Message, cb func(chunk string), onDone func()) (bool, error) {
	// The maximum iterations check is still handled by passing state recursively, but since
	// we spawn goroutines for tools and exit immediately, we shouldn't use a for loop for the async path.
	// We'll process exactly ONE LLM response per call to `runIteration`.
	// The recursion limit would ideally be passed down, but for now we trust the tool loop.

	logger.DebugCF("livekit", "AgentBridge iteration", map[string]any{
		"session": sessionKey,
	})
	select {
	case <-ctx.Done():
		if onDone != nil {
			onDone()
		}
		return false, ctx.Err()
	default:
	}

	toolDefs := ab.toolDefs()
	resp, err := ab.callLLM(ctx, messages, toolDefs, cb)
	if err != nil {
		if onDone != nil {
			onDone()
		}
		return false, err
	}

	assistantMsg := providers.Message{
		Role:    "assistant",
		Content: resp.Content,
	}

	normalized := normalizeToolCalls(resp.ToolCalls)
	if len(normalized) > 0 {
		for _, tc := range normalized {
			argsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	messages = append(messages, assistantMsg)
	if ab.sessions != nil {
		ab.sessions.AddFullMessage(sessionKey, assistantMsg)
	}

	if len(normalized) == 0 {
		// No tools, we are completely done.
		if ab.sessions != nil {
			_ = ab.sessions.Save(sessionKey)
		}
		if onDone != nil {
			onDone()
		}
		return false, nil
	}

	logger.InfoCF("livekit", "Tool calls requested", map[string]any{
		"count":   len(normalized),
		"session": sessionKey,
	})

	// Notify UI of the tool call action
	for _, tc := range normalized {
		if ab.cfg != nil {
			if feedback, ok := ab.cfg.LiveKitService.ToolFeedback[tc.Name]; ok {
				cb(feedback)
			}
		}
	}

	// Run tools asynchronously
	go func(asyncCtx context.Context, asyncSessionKey string, asyncMessages []providers.Message, asyncCalls []providers.ToolCall, asyncCb func(chunk string), asyncOnDone func()) {
		// We use background context for the tool execution because we don't want the tool to die if the turn context cancels early.
		// Ideally we should use a bounded context tied to the overall room session.
		toolCtx := context.Background()
		for _, tc := range asyncCalls {
			result := ab.executeTool(toolCtx, asyncSessionKey, tc)
			toolMsg := providers.Message{
				Role:       "tool",
				Content:    result.ContentForLLM(),
				ToolCallID: tc.ID,
			}
			asyncMessages = append(asyncMessages, toolMsg)
			if ab.sessions != nil {
				ab.sessions.AddFullMessage(asyncSessionKey, toolMsg)
			}
		}

		// trigger the next iteration after tools complete
		// IMPORTANT: We use the asyncCtx (the original cancelable turn context) for the next iteration
		// so that if the user starts speaking, the next iteration's LLM call is cancelled.
		_, err := ab.runIteration(asyncCtx, asyncSessionKey, asyncMessages, asyncCb, asyncOnDone)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorCF("livekit", "Async tool iteration failed", map[string]any{
				"error":   err.Error(),
				"session": asyncSessionKey,
			})
		}
	}(ctx, sessionKey, messages, normalized, cb, onDone)

	// Return true to indicate that async tools are pending.
	return true, nil
}

func (ab *AgentBridge) buildMessages(history []providers.Message, summary, text, sessionKey string) []providers.Message {
	if ab.contextBuilder != nil {
		return ab.contextBuilder.BuildMessages(history, summary, text, nil, "livekit", sessionKey, "", "")
	}

	messages := []providers.Message{}
	if summary != "" {
		messages = append(messages, providers.Message{
			Role:    "system",
			Content: summary,
		})
	}
	messages = append(messages, history...)
	if strings.TrimSpace(text) != "" {
		messages = append(messages, providers.Message{Role: "user", Content: text})
	}
	return messages
}

func (ab *AgentBridge) toolDefs() []providers.ToolDefinition {
	if ab.tools == nil {
		return nil
	}
	return ab.tools.ToProviderDefs()
}

func (ab *AgentBridge) callLLM(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	cb func(chunk string),
) (*providers.LLMResponse, error) {
	if ab.streamProvider != nil {
		lastLen := 0
		resp, err := ab.streamProvider.ChatStream(ctx, messages, toolDefs, ab.modelID, ab.llmOptions, func(acc string) {
			if cb == nil {
				return
			}
			if len(acc) <= lastLen {
				return
			}
			chunk := acc[lastLen:]
			lastLen = len(acc)
			if chunk != "" {
				cb(chunk)
			}
		})
		if err != nil {
			return nil, err
		}
		return resp, nil
	}

	resp, err := ab.provider.Chat(ctx, messages, toolDefs, ab.modelID, ab.llmOptions)
	if err != nil {
		return nil, err
	}
	if cb != nil && resp.Content != "" {
		cb(resp.Content)
	}
	return resp, nil
}

func (ab *AgentBridge) executeTool(ctx context.Context, sessionKey string, tc providers.ToolCall) *tools.ToolResult {
	if ab.tools == nil {
		return tools.ErrorResult("No tools available")
	}
	return ab.tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, "livekit", sessionKey, nil)
}

func normalizeToolCalls(calls []providers.ToolCall) []providers.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]providers.ToolCall, 0, len(calls))
	for _, tc := range calls {
		out = append(out, providers.NormalizeToolCall(tc))
	}
	return out
}
