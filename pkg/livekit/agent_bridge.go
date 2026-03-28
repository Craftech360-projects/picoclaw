package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
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
func (ab *AgentBridge) ChatStream(ctx context.Context, sessionKey string, text string, cb func(chunk string)) error {
	if ab == nil {
		return errors.New("agent bridge is nil")
	}
	if ab.provider == nil {
		return errors.New("provider is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
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

	for iteration := 0; iteration < ab.maxIterations; iteration++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		toolDefs := ab.toolDefs()
		resp, err := ab.callLLM(ctx, messages, toolDefs, cb)
		if err != nil {
			return err
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
			break
		}

		for _, tc := range normalized {
			result := ab.executeTool(ctx, sessionKey, tc)
			toolMsg := providers.Message{
				Role:       "tool",
				Content:    result.ContentForLLM(),
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolMsg)
			if ab.sessions != nil {
				ab.sessions.AddFullMessage(sessionKey, toolMsg)
			}
		}
	}

	if ab.sessions != nil {
		_ = ab.sessions.Save(sessionKey)
	}

	return nil
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
