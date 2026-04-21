package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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

const (
	chatTypeUser      = 1
	chatTypeAssistant = 2
	maxChatContentLen = 2000
)

// PersistedChatMessage is the serialized chat history shape expected by Manager API.
type PersistedChatMessage struct {
	ChatType  int    `json:"chatType"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

// UsageSnapshot stores aggregate token/latency counters for session-end persistence.
type UsageSnapshot struct {
	SessionDurationSeconds      float64
	MessageCount                int
	AvgTTFTSeconds              float64
	TotalResponseDurationSecond float64
	InputTokens                 int
	InputAudioTokens            int
	InputTextTokens             int
	InputCachedTokens           int
	OutputTokens                int
	OutputAudioTokens           int
	OutputTextTokens            int
	TotalTokens                 int
}

type usageState struct {
	sessionStart                time.Time
	messageCount                int
	totalResponseDurationSecond float64
	inputTokens                 int
	inputTextTokens             int
	outputTokens                int
	outputTextTokens            int
}

// AgentBridge provides a simplified agent execution path for voice conversations.
type AgentBridge struct {
	agentInstance     *agent.AgentInstance
	cfg               *config.Config
	provider          providers.LLMProvider
	streamProvider    providers.StreamingProvider
	preserveWorkspace bool
	modelID           string
	sessions          session.SessionStore
	tools             *tools.ToolRegistry
	contextBuilder    *agent.ContextBuilder
	maxIterations     int
	llmOptions        map[string]any
	asyncEventChan    chan AsyncEvent // receives background task completions

	// summarization config
	summarizeMessageThreshold int
	contextWindow             int
	summarizing               sync.Map

	usageMu    sync.Mutex
	usage      usageState
	historyMu  sync.Mutex
	historyLog []PersistedChatMessage
}

// AgentBridgeConfig defines shared resources for creating bridges.
type AgentBridgeConfig struct {
	Config            *config.Config
	Provider          providers.LLMProvider
	ModelID           string
	AgentInstance     *agent.AgentInstance
	PreserveWorkspace bool
	MaxIterations     int
	LLMOptions        map[string]any
	AsyncEventChan    chan AsyncEvent // optional channel for background task results
	// SummarizeMessageThreshold is the number of history messages that triggers summarization.
	// Defaults to 20 if zero.
	SummarizeMessageThreshold int
	// ContextWindow is the model's context size in tokens (approximate), used to detect
	// when the token budget is being approached. Defaults to 128000 if zero.
	ContextWindow int
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
	summarizeThreshold := cfg.SummarizeMessageThreshold
	if summarizeThreshold <= 0 {
		summarizeThreshold = 20
	}
	ctxWindow := cfg.ContextWindow
	if ctxWindow <= 0 {
		ctxWindow = 128000
	}
	ab := &AgentBridge{
		agentInstance:             cfg.AgentInstance,
		cfg:                       cfg.Config,
		provider:                  cfg.Provider,
		modelID:                   cfg.ModelID,
		preserveWorkspace:         cfg.PreserveWorkspace,
		sessions:                  cfg.AgentInstance.Sessions,
		tools:                     cfg.AgentInstance.Tools,
		contextBuilder:            cfg.AgentInstance.ContextBuilder,
		maxIterations:             cfg.MaxIterations,
		llmOptions:                cfg.LLMOptions,
		asyncEventChan:            asyncChan,
		summarizeMessageThreshold: summarizeThreshold,
		contextWindow:             ctxWindow,
	}
	ab.usage.sessionStart = time.Now()
	if sp, ok := cfg.Provider.(providers.StreamingProvider); ok {
		ab.streamProvider = sp
	}
	if ab.maxIterations <= 0 {
		ab.maxIterations = 10
	}
	return ab, nil
}

// Close gracefully releases the session memory store and conditionally cleans
// the active workspace.
func (ab *AgentBridge) Close() {
	if ab.agentInstance != nil {
		ab.agentInstance.Close()
		// Default behavior is ephemeral cleanup unless persistence is requested.
		if !ab.preserveWorkspace && ab.agentInstance.Workspace != "" {
			os.RemoveAll(ab.agentInstance.Workspace)
		}
	}
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
	if strings.TrimSpace(text) != "" {
		ab.recordTranscript(chatTypeUser, text)
	}
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
	callStarted := time.Now()
	resp, err := ab.callLLM(ctx, messages, toolDefs, cb)
	if err != nil {
		if onDone != nil {
			onDone()
		}
		return false, err
	}
	ab.recordUsage(resp.Usage, time.Since(callStarted))

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
	ab.recordTranscript(chatTypeAssistant, assistantMsg.Content)
	if ab.sessions != nil {
		ab.sessions.AddFullMessage(sessionKey, assistantMsg)
	}

	if len(normalized) == 0 {
		// No tools, we are completely done.
		if ab.sessions != nil {
			_ = ab.sessions.Save(sessionKey)
			go ab.maybeSummarize(sessionKey)
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

	// Build a user message with the background task result so the LLM knows it finished
	resultContent := evt.Result.ContentForLLM()
	taskResultMsg := providers.Message{
		Role:    "user",
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

// GenerateGreeting triggers the LLM to dynamically generate an introductory greeting
// when the user connects to the room. It leverages the agent's system prompt to decide the persona.
func (ab *AgentBridge) GenerateGreeting(ctx context.Context, sessionKey string, cb func(chunk string), onDone func()) error {
	if ab == nil || ab.provider == nil {
		if onDone != nil {
			onDone()
		}
		return errors.New("agent bridge or provider is nil")
	}

	greetingPrompt := providers.Message{
		Role:    "user",
		Content: "[System Event] The user has successfully connected to the room and is now listening. Please proactively introduce yourself and greet them using your persona guidelines.",
	}

	if ab.sessions != nil {
		ab.sessions.AddFullMessage(sessionKey, greetingPrompt)
	}

	var history []providers.Message
	var summary string
	if ab.sessions != nil {
		history = ab.sessions.GetHistory(sessionKey)
		summary = ab.sessions.GetSummary(sessionKey)
	}
	messages := ab.buildMessages(history, summary, "", sessionKey)

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

// maybeSummarize triggers background context summarization when history grows past
// the configured message threshold (default 20). This mirrors AgentLoop.maybeSummarize
// but is self-contained so AgentBridge doesn't need a reference to AgentLoop.
func (ab *AgentBridge) maybeSummarize(sessionKey string) {
	if ab.sessions == nil {
		return
	}
	history := ab.sessions.GetHistory(sessionKey)
	if len(history) <= ab.summarizeMessageThreshold {
		return
	}

	// Deduplicate: only run one summarization per session at a time
	if _, alreadyRunning := ab.summarizing.LoadOrStore(sessionKey, true); alreadyRunning {
		return
	}
	defer ab.summarizing.Delete(sessionKey)

	// Wait for 5 seconds to ensure the user has actually stopped speaking.
	// This prevents the background summarization LLM call from hogging
	// API connection pools/rate limits while the main voice loop is active.
	time.Sleep(5 * time.Second)

	// If the user spoke or was responded to during our 5s sleep, abort!
	// We only summarize when the conversation is completely idle.
	currentHistory := ab.sessions.GetHistory(sessionKey)
	if len(currentHistory) > len(history) {
		logger.DebugCF("livekit", "Aborting summarization, conversation active", map[string]any{
			"session": sessionKey,
		})
		return
	}

	// Revert to working on the latest history
	history = currentHistory

	logger.InfoCF("livekit", "Voice context threshold reached, summarizing history", map[string]any{
		"session":  sessionKey,
		"messages": len(history),
		"limit":    ab.summarizeMessageThreshold,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Keep the 4 most recent messages for continuity; summarize the rest.
	if len(history) <= 4 {
		return
	}
	cutAt := len(history) - 4
	// Align to a user-message boundary so we never split a tool sequence.
	for cutAt > 0 && history[cutAt-1].Role != "user" {
		cutAt--
	}
	if cutAt <= 0 {
		cutAt = len(history) - 4
	}

	toSummarize := history[:cutAt]
	keepCount := len(history) - cutAt

	// Only include user/assistant messages in the summarization prompt.
	var batch []providers.Message
	for _, m := range toSummarize {
		if m.Role == "user" || m.Role == "assistant" {
			batch = append(batch, m)
		}
	}
	if len(batch) == 0 {
		return
	}

	existingSummary := ab.sessions.GetSummary(sessionKey)
	newSummary, err := ab.bridgeSummarizeBatch(ctx, batch, existingSummary)
	if err != nil || newSummary == "" {
		logger.WarnCF("livekit", "Voice context summarization failed", map[string]any{
			"session": sessionKey,
			"error":   fmt.Sprintf("%v", err),
		})
		return
	}

	ab.sessions.SetSummary(sessionKey, newSummary)
	ab.sessions.TruncateHistory(sessionKey, keepCount)
	_ = ab.sessions.Save(sessionKey)

	logger.InfoCF("livekit", "Voice context summarized", map[string]any{
		"session":           sessionKey,
		"summarized_msgs":   len(batch),
		"kept_msgs":         keepCount,
		"new_summary_bytes": len(newSummary),
	})
}

// bridgeSummarizeBatch calls the LLM to produce a concise summary of a batch of messages.
func (ab *AgentBridge) bridgeSummarizeBatch(ctx context.Context, batch []providers.Message, existingSummary string) (string, error) {
	var sb strings.Builder
	sb.WriteString("Provide a concise summary of this conversation segment, preserving core context and key points.\n")
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}

	prompt := sb.String()
	msg := []providers.Message{{Role: "user", Content: prompt}}
	opts := map[string]any{"temperature": 0.3}

	resp, err := ab.provider.Chat(ctx, msg, nil, ab.modelID, opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (ab *AgentBridge) recordTranscript(chatType int, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if len(content) > maxChatContentLen {
		content = content[:maxChatContentLen]
	}
	if chatType != chatTypeUser && chatType != chatTypeAssistant {
		chatType = chatTypeAssistant
	}

	ab.historyMu.Lock()
	ab.historyLog = append(ab.historyLog, PersistedChatMessage{
		ChatType:  chatType,
		Content:   content,
		Timestamp: time.Now().Unix(),
	})
	ab.historyMu.Unlock()
}

func (ab *AgentBridge) recordUsage(usage *providers.UsageInfo, elapsed time.Duration) {
	ab.usageMu.Lock()
	defer ab.usageMu.Unlock()

	ab.usage.messageCount++
	ab.usage.totalResponseDurationSecond += elapsed.Seconds()
	if usage == nil {
		return
	}

	ab.usage.inputTokens += usage.PromptTokens
	ab.usage.outputTokens += usage.CompletionTokens
	ab.usage.inputTextTokens += usage.PromptTokens
	ab.usage.outputTextTokens += usage.CompletionTokens
}

// UsageSnapshot returns aggregated usage counters for this voice session.
func (ab *AgentBridge) UsageSnapshot() UsageSnapshot {
	ab.usageMu.Lock()
	defer ab.usageMu.Unlock()

	duration := 0.0
	if !ab.usage.sessionStart.IsZero() {
		duration = time.Since(ab.usage.sessionStart).Seconds()
	}
	total := ab.usage.inputTokens + ab.usage.outputTokens
	return UsageSnapshot{
		SessionDurationSeconds:      duration,
		MessageCount:                ab.usage.messageCount,
		AvgTTFTSeconds:              0,
		TotalResponseDurationSecond: ab.usage.totalResponseDurationSecond,
		InputTokens:                 ab.usage.inputTokens,
		InputAudioTokens:            0,
		InputTextTokens:             ab.usage.inputTextTokens,
		InputCachedTokens:           0,
		OutputTokens:                ab.usage.outputTokens,
		OutputAudioTokens:           0,
		OutputTextTokens:            ab.usage.outputTextTokens,
		TotalTokens:                 total,
	}
}

// TranscriptSnapshot returns a defensive copy of the conversation transcript.
func (ab *AgentBridge) TranscriptSnapshot() []PersistedChatMessage {
	ab.historyMu.Lock()
	defer ab.historyMu.Unlock()

	out := make([]PersistedChatMessage, len(ab.historyLog))
	copy(out, ab.historyLog)
	return out
}
