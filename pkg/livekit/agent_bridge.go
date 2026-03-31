package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AsyncEvent represents a completed background task result that needs to be
// announced to the user via spontaneous speech.
type AsyncEvent struct {
	SessionKey string
	ToolName   string
	Result     *tools.ToolResult
}

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
	asyncEventChan chan AsyncEvent // receives background task completions
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
	AsyncEventChan chan AsyncEvent // optional channel for background task results
}

// NewAgentBridge creates a new AgentBridge.
func NewAgentBridge(cfg AgentBridgeConfig) (*AgentBridge, error) {
	if cfg.Provider == nil {
		return nil, errors.New("provider is nil")
	}
	asyncChan := cfg.AsyncEventChan
	if asyncChan == nil {
		asyncChan = make(chan AsyncEvent, 16)
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
		asyncEventChan: asyncChan,
	}
	if sp, ok := cfg.Provider.(providers.StreamingProvider); ok {
		ab.streamProvider = sp
	}
	if ab.maxIterations <= 0 {
		ab.maxIterations = 10
	}
	return ab, nil
}

// AsyncEvents returns the channel that receives background task completion events.
func (ab *AgentBridge) AsyncEvents() <-chan AsyncEvent {
	if ab == nil {
		return nil
	}
	return ab.asyncEventChan
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
	var messages []providers.Message
	if ab.contextBuilder != nil {
		messages = ab.contextBuilder.BuildMessages(history, summary, text, nil, "livekit", sessionKey, "", "")
	} else {
		messages = []providers.Message{}
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
	}

	// Inject voice-mode directives. This is appended as a system message AFTER the
	// main system prompt so it takes priority. It prevents the LLM from narrating
	// long generated content (songs, code, etc.) through TTS.
	voiceDirective := providers.Message{
		Role: "system",
		Content: `## Voice Mode Active
You are speaking to the user through a voice interface (text-to-speech).

CRITICAL RULES FOR VOICE:
1. Keep ALL responses SHORT and conversational — 1-3 sentences max.
2. NEVER read out long content (songs, code, poems, lists, file contents) aloud. Instead, silently write it to a file using tools and tell the user where you saved it.
3. NEVER use markdown formatting (**, *, #, backticks, bullet points). Speak in plain natural language.
4. When using tools like write_file or spawn, do NOT narrate or preview the content. Just do it and briefly confirm.
5. Avoid reading file paths character by character. Say "I saved it to your workspace" instead.`,
	}

	// Insert voice directive right after the first system message (if any)
	inserted := false
	for i, msg := range messages {
		if msg.Role == "system" {
			// Insert after the first system message
			result := make([]providers.Message, 0, len(messages)+1)
			result = append(result, messages[:i+1]...)
			result = append(result, voiceDirective)
			result = append(result, messages[i+1:]...)
			messages = result
			inserted = true
			break
		}
	}
	if !inserted {
		// No system message found — prepend the voice directive
		messages = append([]providers.Message{voiceDirective}, messages...)
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

	// Create an async callback that routes completed background tasks to the
	// asyncEventChan, so the audio pipeline can announce results spontaneously.
	asyncCb := tools.AsyncCallback(func(_ context.Context, result *tools.ToolResult) {
		if ab.asyncEventChan == nil || result == nil {
			return
		}
		logger.InfoCF("livekit", "Background task completed, sending async event", map[string]any{
			"tool":    tc.Name,
			"session": sessionKey,
		})
		select {
		case ab.asyncEventChan <- AsyncEvent{
			SessionKey: sessionKey,
			ToolName:   tc.Name,
			Result:     result,
		}:
		default:
			logger.WarnCF("livekit", "Async event channel full, dropping background task result", map[string]any{
				"tool":    tc.Name,
				"session": sessionKey,
			})
		}
	})

	return ab.tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, "livekit", sessionKey, asyncCb)
}

// GenerateSpontaneousResponse triggers a spontaneous LLM response for a background
// task result. It appends the result to conversation history and streams a response.
func (ab *AgentBridge) GenerateSpontaneousResponse(ctx context.Context, sessionKey string, evt AsyncEvent, cb func(chunk string), onDone func()) error {
	if ab == nil || ab.provider == nil {
		if onDone != nil {
			onDone()
		}
		return errors.New("agent bridge or provider is nil")
	}

	// Build a system message with the background task result
	resultContent := evt.Result.ContentForLLM()
	taskResultMsg := providers.Message{
		Role:    "system",
		Content: fmt.Sprintf("[Background Task Completed] Tool '%s' finished with result: %s\n\nPlease briefly announce this result to the user in a natural, conversational way.", evt.ToolName, resultContent),
	}

	// Add the result to conversation history
	if ab.sessions != nil {
		ab.sessions.AddFullMessage(sessionKey, taskResultMsg)
	}

	// Build messages with history + task result
	var history []providers.Message
	var summary string
	if ab.sessions != nil {
		history = ab.sessions.GetHistory(sessionKey)
		summary = ab.sessions.GetSummary(sessionKey)
	}
	messages := ab.buildMessages(history, summary, "", sessionKey)

	// Call LLM to generate the announcement
	_, err := ab.runIteration(ctx, sessionKey, messages, cb, onDone)
	return err
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
