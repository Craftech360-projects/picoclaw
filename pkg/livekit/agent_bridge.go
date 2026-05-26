package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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

// RuntimeEvent is a lightweight structured observability event for LiveKit
// runtime lifecycle tracking.
type RuntimeEvent struct {
	Kind        string         `json:"kind"`
	TimestampMS int64          `json:"timestamp_ms"`
	SessionKey  string         `json:"session_key,omitempty"`
	ToolName    string         `json:"tool_name,omitempty"`
	Attempt     int            `json:"attempt,omitempty"`
	Cause       string         `json:"cause,omitempty"`
	Error       string         `json:"error,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

const (
	chatTypeUser        = 1
	chatTypeAssistant   = 2
	maxChatContentLen   = 2000
	maxToolDefsForLLM   = 20
	maxGemini429Retries = 2
	maxLLMLogContentLen = 1200
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

// SessionQualityReport is an aggregate quality summary for one voice session.
type SessionQualityReport struct {
	FallbackCount            int     `json:"fallback_count"`
	InterruptionCount        int     `json:"interruption_count"`
	InterruptionRecovered    int     `json:"interruption_recovered"`
	InterruptionRecoveryRate float64 `json:"interruption_recovery_rate"`
	ErrorCount               int     `json:"error_count"`
	RetryCount               int     `json:"retry_count"`
	MedianTTFTMs             int64   `json:"median_ttft_ms"`
	AvgTTFTMs                int64   `json:"avg_ttft_ms"`
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

type qualityState struct {
	fallbackCount         int
	interruptionCount     int
	interruptionRecovered int
	interruptionPending   bool
	errorCount            int
	retryCount            int
	ttftSamplesMS         []int64
}

type MCPManager interface {
	Close() error
}

// AgentBridge provides a simplified agent execution path for voice conversations.
type AgentBridge struct {
	agentInstance       *agent.AgentInstance
	cfg                 *config.Config
	provider            providers.LLMProvider
	streamProvider      providers.StreamingProvider
	preserveWorkspace   bool
	onClose             func()
	onAfterClose        func()
	modelID             string
	sessions            session.SessionStore
	tools               *tools.ToolRegistry
	contextBuilder      *agent.ContextBuilder
	maxIterations       int
	llmOptions          map[string]any
	proactiveLLMOptions map[string]any
	asyncEventChan      chan AsyncEvent // receives background task completions
	runtimeEventChan    chan RuntimeEvent
	workspaceArtifacts  WorkspaceArtifactStore
	mcpManager          MCPManager
	allowedToolNames    map[string]struct{}

	// summarization config
	summarizeMessageThreshold int
	contextWindow             int
	summarizing               sync.Map

	usageMu    sync.Mutex
	usage      usageState
	historyMu  sync.Mutex
	historyLog []PersistedChatMessage
	qualityMu  sync.Mutex
	quality    qualityState
	traceMu    sync.Mutex
	traceLog   []RuntimeEvent

	sessionLanguageMu   sync.RWMutex
	sessionLanguageName string
	sessionLanguageCode string
	languageLockEnabled bool

	closeMu sync.Mutex
	closed  bool

	sessionLLMLocks sync.Map // map[string]*sync.Mutex
}

// AgentBridgeConfig defines shared resources for creating bridges.
type AgentBridgeConfig struct {
	Config             *config.Config
	Provider           providers.LLMProvider
	ModelID            string
	AgentInstance      *agent.AgentInstance
	PreserveWorkspace  bool
	OnClose            func()
	OnAfterClose       func()
	MaxIterations      int
	LLMOptions         map[string]any
	AsyncEventChan     chan AsyncEvent // optional channel for background task results
	RuntimeEventChan   chan RuntimeEvent
	WorkspaceArtifacts WorkspaceArtifactStore
	MCPManager         MCPManager
	// SummarizeMessageThreshold is the number of history messages that triggers summarization.
	// Defaults to 20 if zero.
	SummarizeMessageThreshold int
	// ContextWindow is the model's context size in tokens (approximate), used to detect
	// when the token budget is being approached. Defaults to 128000 if zero.
	ContextWindow int

	SessionLanguageName string
	SessionLanguageCode string
	LanguageLockEnabled bool
	AllowedToolNames    []string
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
	runtimeChan := cfg.RuntimeEventChan
	if runtimeChan == nil {
		runtimeChan = make(chan RuntimeEvent, 128)
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
		onClose:                   cfg.OnClose,
		onAfterClose:              cfg.OnAfterClose,
		sessions:                  cfg.AgentInstance.Sessions,
		tools:                     cfg.AgentInstance.Tools,
		contextBuilder:            cfg.AgentInstance.ContextBuilder,
		maxIterations:             cfg.MaxIterations,
		llmOptions:                cfg.LLMOptions,
		proactiveLLMOptions:       buildProactiveLLMOptions(cfg.LLMOptions),
		asyncEventChan:            asyncChan,
		runtimeEventChan:          runtimeChan,
		workspaceArtifacts:        cfg.WorkspaceArtifacts,
		mcpManager:                cfg.MCPManager,
		summarizeMessageThreshold: summarizeThreshold,
		contextWindow:             ctxWindow,
		languageLockEnabled:       cfg.LanguageLockEnabled,
		allowedToolNames:          normalizeAllowedToolNames(cfg.AllowedToolNames),
	}
	policy := NormalizeSessionLanguagePolicy(cfg.SessionLanguageName, cfg.SessionLanguageCode)
	ab.sessionLanguageName = policy.DisplayName
	ab.sessionLanguageCode = policy.RawCode
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
	ab.closeMu.Lock()
	if ab.closed {
		ab.closeMu.Unlock()
		return
	}
	ab.closed = true
	runtimeEventChan := ab.runtimeEventChan
	ab.runtimeEventChan = nil
	mcpManager := ab.mcpManager
	ab.mcpManager = nil
	ab.closeMu.Unlock()

	if runtimeEventChan != nil {
		close(runtimeEventChan)
	}
	if mcpManager != nil {
		if err := mcpManager.Close(); err != nil {
			logger.WarnCF("livekit", "Failed to close MCP manager",
				map[string]any{"error": err.Error()})
		}
	}
	if ab.agentInstance != nil {
		ab.agentInstance.Close()
		workspace := strings.TrimSpace(ab.agentInstance.Workspace)
		// Run OnClose before workspace deletion so callbacks can still read files.
		// OnAfterClose is always executed last (for lock release / final handoff).
		defer func() {
			if ab.onAfterClose != nil {
				ab.onAfterClose()
			}
		}()
		if ab.onClose != nil {
			ab.onClose()
		}
		// Default behavior is ephemeral cleanup unless persistence is requested.
		if !ab.preserveWorkspace && workspace != "" {
			if ok, owner := HasRecentWorkspaceReconnectHint(workspace, 45*time.Second); ok {
				logger.InfoCF("livekit", "Skipping workspace delete due to reconnect handoff hint", map[string]any{
					"workspace": workspace,
					"owner":     owner,
				})
			} else if err := os.RemoveAll(workspace); err != nil {
				logger.WarnCF("livekit", "Failed to delete workspace during bridge close", map[string]any{
					"workspace": workspace,
					"error":     err.Error(),
				})
			}
		}
	}
}

// AttachMCPManager safely binds a late-initialized MCP manager to the bridge.
// It returns false when the bridge is already closed; in that case the provided
// manager is closed immediately to avoid leaks.
func (ab *AgentBridge) AttachMCPManager(manager MCPManager) bool {
	if ab == nil || manager == nil {
		return false
	}

	ab.closeMu.Lock()
	if ab.closed {
		ab.closeMu.Unlock()
		if err := manager.Close(); err != nil {
			logger.WarnCF("livekit", "Failed to close MCP manager after late attach on closed bridge",
				map[string]any{"error": err.Error()})
		}
		return false
	}
	if ab.mcpManager != nil {
		ab.closeMu.Unlock()
		if err := manager.Close(); err != nil {
			logger.WarnCF("livekit", "Failed to close redundant MCP manager attach",
				map[string]any{"error": err.Error()})
		}
		return false
	}
	ab.mcpManager = manager
	ab.closeMu.Unlock()
	return true
}

// AsyncEvents returns the channel that receives background task completion events.
func (ab *AgentBridge) AsyncEvents() <-chan AsyncEvent {
	if ab == nil {
		return nil
	}
	return ab.asyncEventChan
}

// RuntimeEvents returns the channel that carries structured runtime events.
func (ab *AgentBridge) RuntimeEvents() <-chan RuntimeEvent {
	if ab == nil {
		return nil
	}
	return ab.runtimeEventChan
}

// EmitRuntimeEvent publishes a runtime event in a non-blocking way.
func (ab *AgentBridge) EmitRuntimeEvent(evt RuntimeEvent) bool {
	if ab == nil || ab.runtimeEventChan == nil {
		return false
	}
	if evt.TimestampMS == 0 {
		evt.TimestampMS = time.Now().UnixMilli()
	}
	ab.recordRuntimeQuality(evt)
	ab.recordTraceEvent(evt)
	select {
	case ab.runtimeEventChan <- evt:
		return true
	default:
		return false
	}
}

func (ab *AgentBridge) recordTraceEvent(evt RuntimeEvent) {
	if ab == nil {
		return
	}
	ab.traceMu.Lock()
	defer ab.traceMu.Unlock()
	ab.traceLog = append(ab.traceLog, evt)
	const maxTraceEvents = 4000
	if len(ab.traceLog) > maxTraceEvents {
		ab.traceLog = append([]RuntimeEvent(nil), ab.traceLog[len(ab.traceLog)-maxTraceEvents:]...)
	}
}

func (ab *AgentBridge) recordRuntimeQuality(evt RuntimeEvent) {
	if ab == nil {
		return
	}
	ab.qualityMu.Lock()
	defer ab.qualityMu.Unlock()

	switch evt.Kind {
	case "fallback_used":
		ab.quality.fallbackCount++
	case "retry_scheduled":
		ab.quality.retryCount++
	case "turn_error":
		ab.quality.errorCount++
		if ab.quality.interruptionPending {
			ab.quality.interruptionPending = false
		}
	case "interruption":
		ab.quality.interruptionCount++
		ab.quality.interruptionPending = true
	case "turn_end":
		if ab.quality.interruptionPending && evt.Cause == "assistant_response_complete" {
			ab.quality.interruptionRecovered++
			ab.quality.interruptionPending = false
		}
	case "latency_marker":
		if evt.Cause != "llm_first_token" || evt.Metadata == nil {
			return
		}
		if raw, ok := evt.Metadata["elapsed_ms"]; ok {
			switch v := raw.(type) {
			case int64:
				ab.quality.ttftSamplesMS = append(ab.quality.ttftSamplesMS, v)
			case int:
				ab.quality.ttftSamplesMS = append(ab.quality.ttftSamplesMS, int64(v))
			case float64:
				ab.quality.ttftSamplesMS = append(ab.quality.ttftSamplesMS, int64(v))
			}
		}
	}
}

// EnqueueAsyncEvent publishes a background event to the bridge async queue.
// Returns false when the bridge is nil, the queue is unavailable, or the queue is full.
func (ab *AgentBridge) EnqueueAsyncEvent(evt AsyncEvent) bool {
	if ab == nil || ab.asyncEventChan == nil {
		return false
	}
	select {
	case ab.asyncEventChan <- evt:
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "background_event_enqueued",
			SessionKey: evt.SessionKey,
			ToolName:   evt.ToolName,
		})
		return true
	default:
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "background_event_dropped",
			SessionKey: evt.SessionKey,
			ToolName:   evt.ToolName,
			Cause:      "queue_full",
		})
		return false
	}
}

func (ab *AgentBridge) RealtimeChatPersistenceEnabled() bool {
	if ab == nil || ab.sessions == nil {
		return false
	}
	marker, ok := ab.sessions.(session.RealtimeChatPersistenceMarker)
	return ok && marker.RealtimeChatPersistenceEnabled()
}

// FinalizeSessionSummary summarizes the completed voice session even when the
// rolling context threshold was not reached during the call.
func (ab *AgentBridge) FinalizeSessionSummary(ctx context.Context, sessionKey string) (string, int, error) {
	if ab == nil || ab.sessions == nil {
		return "", 0, nil
	}

	history := ab.sessions.GetHistory(sessionKey)
	if len(history) == 0 {
		for _, msg := range ab.TranscriptSnapshot() {
			role := "assistant"
			if msg.ChatType == chatTypeUser {
				role = "user"
			}
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			history = append(history, providers.Message{Role: role, Content: msg.Content})
		}
	}

	batch := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		if (msg.Role == "user" || msg.Role == "assistant") && strings.TrimSpace(msg.Content) != "" {
			batch = append(batch, msg)
		}
	}
	if len(batch) == 0 {
		return ab.sessions.GetSummary(sessionKey), 0, nil
	}

	existingSummary := ab.sessions.GetSummary(sessionKey)
	newSummary, err := ab.bridgeSummarizeBatch(ctx, batch, existingSummary)
	if err != nil || strings.TrimSpace(newSummary) == "" {
		return "", len(batch), err
	}

	ab.sessions.SetSummary(sessionKey, newSummary)
	_ = ab.sessions.Save(sessionKey)
	return newSummary, len(batch), nil
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
	return ab.runIterationWithProfile(ctx, sessionKey, messages, cb, onDone, "conversation")
}

func (ab *AgentBridge) runIterationWithProfile(ctx context.Context, sessionKey string, messages []providers.Message, cb func(chunk string), onDone func(), profile string) (bool, error) {
	// The maximum iterations check is still handled by passing state recursively, but since
	// we spawn goroutines for tools and exit immediately, we shouldn't use a for loop for the async path.
	// We'll process exactly ONE LLM response per call to `runIteration`.
	// The recursion limit would ideally be passed down, but for now we trust the tool loop.

	logger.DebugCF("livekit", "AgentBridge iteration", map[string]any{
		"session": sessionKey,
		"profile": profile,
	})
	ab.EmitRuntimeEvent(RuntimeEvent{
		Kind:       "turn_start",
		SessionKey: sessionKey,
		Metadata: map[string]any{
			"message_count": len(messages),
			"profile":       profile,
		},
	})
	select {
	case <-ctx.Done():
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "turn_end",
			SessionKey: sessionKey,
			Cause:      "context_canceled",
		})
		if onDone != nil {
			onDone()
		}
		return false, ctx.Err()
	default:
	}

	toolDefs := ab.toolDefs()
	releaseLLMSlot, err := ab.acquireSessionLLMSlot(ctx, sessionKey)
	if err != nil {
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "turn_error",
			SessionKey: sessionKey,
			Error:      err.Error(),
			Cause:      "llm_slot_wait_canceled",
		})
		if onDone != nil {
			onDone()
		}
		return false, err
	}
	defer releaseLLMSlot()

	callStarted := time.Now()
	resp, err := ab.callLLM(ctx, sessionKey, messages, toolDefs, ab.optionsForProfile(profile), cb)
	if err != nil {
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "turn_error",
			SessionKey: sessionKey,
			Error:      err.Error(),
			Cause:      "llm_call_failed",
		})
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
	logger.InfoCF("livekit", "LLM response received", map[string]any{
		"session":         sessionKey,
		"profile":         profile,
		"content":         trimForLog(resp.Content, maxLLMLogContentLen),
		"content_len":     len(resp.Content),
		"tool_call_count": len(resp.ToolCalls),
	})

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
		ab.EmitRuntimeEvent(RuntimeEvent{
			Kind:       "turn_end",
			SessionKey: sessionKey,
			Cause:      "assistant_response_complete",
		})
		return false, nil
	}

	logger.InfoCF("livekit", "Tool calls requested", map[string]any{
		"count":   len(normalized),
		"session": sessionKey,
	})
	ab.EmitRuntimeEvent(RuntimeEvent{
		Kind:       "tool_calls_requested",
		SessionKey: sessionKey,
		Metadata: map[string]any{
			"count": len(normalized),
		},
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
			ab.EmitRuntimeEvent(RuntimeEvent{
				Kind:       "tool_call_start",
				SessionKey: asyncSessionKey,
				ToolName:   tc.Name,
			})
			result := ab.executeTool(toolCtx, asyncSessionKey, tc)
			evt := RuntimeEvent{
				Kind:       "tool_call_end",
				SessionKey: asyncSessionKey,
				ToolName:   tc.Name,
			}
			if result != nil && result.IsError {
				evt.Error = result.ForLLM
				evt.Cause = "tool_error"
			}
			ab.EmitRuntimeEvent(evt)
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
		_, err := ab.runIterationWithProfile(asyncCtx, asyncSessionKey, asyncMessages, asyncCb, asyncOnDone, profile)
		if err != nil && !errors.Is(err, context.Canceled) {
			ab.EmitRuntimeEvent(RuntimeEvent{
				Kind:       "turn_error",
				SessionKey: asyncSessionKey,
				Cause:      "async_iteration_failed",
				Error:      err.Error(),
			})
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
	activeSkills := ab.activeSkillNames()
	if ab.contextBuilder != nil {
		messages = ab.contextBuilder.BuildMessages(history, summary, text, nil, "livekit", sessionKey, "", "", activeSkills...)
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
5. Avoid reading file paths character by character. Say "I saved it to your workspace" instead.
6. For weather requests, use get_weather first.
7. For date/time requests, use get_time_date first.
8. For current or time-sensitive facts such as latest, today, yesterday, 2026 data, scores, schedules, rosters, rankings, weather, news, prices, or team data: do not answer from memory. Use tools and verify.
9. Do not use web_fetch on search result pages like Google search as evidence. Use web_search first, then fetch a real source page from the results.
10. If tools fail, return blocked, or provide too little evidence, say you could not verify it instead of guessing.
11. Never claim abilities that are not available in this voice runtime.
12. If asked what you can do, only mention these capabilities: web_search, web_fetch, get_weather, get_time_date, and memory-aware conversation.
13. Do not say you can run shell/terminal commands, tmux, GitHub actions, create/deploy agents, control a browser, or control hardware devices unless such tools are explicitly available in this runtime.
14. If asked about any unavailable capability, clearly say it is not available in this voice runtime and offer one available capability instead.`,
	}

	// Insert voice directive right after the first system message (if any)
	// Insert voice first, then language lock. Because both are inserted at the
	// same anchor (after the first system message), second insertion gets higher priority.
	messages = insertSystemDirectiveAfterFirstSystem(messages, voiceDirective)
	if ab.languageLockEnabled {
		if directive := strings.TrimSpace(ab.sessionLanguageDirective()); directive != "" {
			messages = insertSystemDirectiveAfterFirstSystem(messages, providers.Message{
				Role:    "system",
				Content: directive,
			})
		}
	}
	// Deterministic priority: base system -> language lock -> voice.

	return messages
}

func insertSystemDirectiveAfterFirstSystem(messages []providers.Message, directive providers.Message) []providers.Message {
	for i, msg := range messages {
		if msg.Role != "system" {
			continue
		}
		result := make([]providers.Message, 0, len(messages)+1)
		result = append(result, messages[:i+1]...)
		result = append(result, directive)
		result = append(result, messages[i+1:]...)
		return result
	}
	return append([]providers.Message{directive}, messages...)
}

func (ab *AgentBridge) sessionLanguageDirective() string {
	policy := ab.SessionLanguagePolicy()
	if strings.TrimSpace(policy.DisplayName) == "" {
		return ""
	}
	return fmt.Sprintf(`## Session Language Override
This session language is fixed by RFID policy.
Speak only in %s unless the user explicitly asks for translation or transliteration.
Do not auto-switch language based on mixed-language input.
Use child-friendly tone and prefer native script for %s by default.`, policy.DisplayName, policy.DisplayName)
}

// UpdateSessionLanguage updates in-memory session language policy atomically.
func (ab *AgentBridge) UpdateSessionLanguage(name, code string) {
	if ab == nil {
		return
	}
	policy := NormalizeSessionLanguagePolicy(name, code)
	ab.sessionLanguageMu.Lock()
	ab.sessionLanguageName = policy.DisplayName
	ab.sessionLanguageCode = policy.RawCode
	ab.sessionLanguageMu.Unlock()
}

// SessionLanguagePolicy returns a snapshot of current language policy.
func (ab *AgentBridge) SessionLanguagePolicy() SessionLanguagePolicy {
	if ab == nil {
		return NormalizeSessionLanguagePolicy("", "")
	}
	ab.sessionLanguageMu.RLock()
	name := ab.sessionLanguageName
	code := ab.sessionLanguageCode
	ab.sessionLanguageMu.RUnlock()
	return NormalizeSessionLanguagePolicy(name, code)
}

func (ab *AgentBridge) activeSkillNames() []string {
	if ab == nil || ab.agentInstance == nil || len(ab.agentInstance.SkillsFilter) == 0 {
		return nil
	}

	out := make([]string, 0, len(ab.agentInstance.SkillsFilter))
	seen := map[string]struct{}{}
	for _, name := range ab.agentInstance.SkillsFilter {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (ab *AgentBridge) toolDefs() []providers.ToolDefinition {
	if ab.tools == nil {
		return nil
	}
	defs := ab.tools.ToProviderDefs()
	defs = ab.filterToolDefsByAllowlist(defs)
	if len(defs) <= maxToolDefsForLLM {
		return defs
	}
	prioritized := prioritizeVoiceToolDefs(defs)
	logger.WarnCF("livekit", "Tool definitions exceed provider limit; truncating for voice turn", map[string]any{
		"configured": len(defs),
		"limit":      maxToolDefsForLLM,
		"priority_kept": []string{
			"get_weather", "get_time_date", "web_search", "web_fetch", "read_file", "write_file", "list_dir",
		},
	})
	return prioritized[:maxToolDefsForLLM]
}

func prioritizeVoiceToolDefs(defs []providers.ToolDefinition) []providers.ToolDefinition {
	if len(defs) <= 1 {
		return defs
	}
	priority := []string{
		"get_weather",
		"get_time_date",
		"web_search",
		"web_fetch",
		"read_file",
		"write_file",
		"list_dir",
	}
	added := make(map[int]struct{}, len(defs))
	out := make([]providers.ToolDefinition, 0, len(defs))

	for _, name := range priority {
		for i := range defs {
			if _, ok := added[i]; ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(defs[i].Function.Name), name) {
				out = append(out, defs[i])
				added[i] = struct{}{}
				break
			}
		}
	}
	for i := range defs {
		if _, ok := added[i]; ok {
			continue
		}
		out = append(out, defs[i])
	}
	return out
}

func (ab *AgentBridge) callLLM(
	ctx context.Context,
	sessionKey string,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	llmOptions map[string]any,
	cb func(chunk string),
) (*providers.LLMResponse, error) {
	if !ab.shouldRetryGeminiRateLimit() {
		return ab.callLLMOnce(ctx, messages, toolDefs, llmOptions, cb)
	}

	backoffs := []time.Duration{300 * time.Millisecond, 900 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt <= maxGemini429Retries; attempt++ {
		streamedAny := false
		wrappedCB := func(chunk string) {
			if strings.TrimSpace(chunk) != "" {
				streamedAny = true
			}
			if cb != nil {
				cb(chunk)
			}
		}
		resp, err := ab.callLLMOnce(ctx, messages, toolDefs, llmOptions, wrappedCB)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt >= maxGemini429Retries || streamedAny || !isRateLimitLLMError(err) {
			break
		}
		delay := applySmallJitter(backoffs[attempt%len(backoffs)])
		logger.WarnCF("livekit", "Gemini rate-limited; retrying LLM call", map[string]any{
			"session":     sessionKey,
			"attempt":     attempt + 1,
			"max_retries": maxGemini429Retries,
			"delay_ms":    delay.Milliseconds(),
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

func (ab *AgentBridge) callLLMOnce(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	llmOptions map[string]any,
	cb func(chunk string),
) (*providers.LLMResponse, error) {
	if ab.streamProvider != nil {
		lastLen := 0
		resp, err := ab.streamProvider.ChatStream(ctx, messages, toolDefs, ab.modelID, llmOptions, func(acc string) {
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

	resp, err := ab.provider.Chat(ctx, messages, toolDefs, ab.modelID, llmOptions)
	if err != nil {
		return nil, err
	}
	if cb != nil && resp.Content != "" {
		cb(resp.Content)
	}
	return resp, nil
}

func (ab *AgentBridge) shouldRetryGeminiRateLimit() bool {
	model := strings.ToLower(strings.TrimSpace(ab.modelID))
	return strings.Contains(model, "gemini")
}

func isRateLimitLLMError(err error) bool {
	if err == nil {
		return false
	}
	lowerErr := strings.ToLower(err.Error())
	return strings.Contains(lowerErr, "429") ||
		strings.Contains(lowerErr, "rate limit") ||
		strings.Contains(lowerErr, "rate-limited") ||
		strings.Contains(lowerErr, "resource exhausted") ||
		strings.Contains(lowerErr, "resource_exhausted")
}

func applySmallJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	jitter := time.Duration(time.Now().UnixNano()%90) * time.Millisecond
	return base + jitter
}

func (ab *AgentBridge) acquireSessionLLMSlot(ctx context.Context, sessionKey string) (func(), error) {
	key := strings.TrimSpace(strings.ToLower(sessionKey))
	if key == "" {
		key = "__global__"
	}
	lockAny, _ := ab.sessionLLMLocks.LoadOrStore(key, &sync.Mutex{})
	mu, _ := lockAny.(*sync.Mutex)
	if mu == nil {
		return func() {}, nil
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if mu.TryLock() {
			return mu.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (ab *AgentBridge) optionsForProfile(profile string) map[string]any {
	if strings.EqualFold(profile, "proactive") {
		return ab.proactiveLLMOptions
	}
	return ab.llmOptions
}

func cloneOptions(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func trimForLog(content string, limit int) string {
	content = strings.TrimSpace(content)
	if limit <= 0 || len(content) <= limit {
		return content
	}
	return content[:limit] + "...(truncated)"
}

func toolArgsPreview(args map[string]any, limit int) string {
	if len(args) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("<unmarshalable args: %v>", err)
	}
	return trimForLog(string(raw), limit)
}

func buildProactiveLLMOptions(base map[string]any) map[string]any {
	out := cloneOptions(base)
	if _, ok := out["max_tokens"]; !ok {
		out["max_tokens"] = 220
	}
	if _, ok := out["temperature"]; !ok {
		out["temperature"] = 0.4
	}
	return out
}

func (ab *AgentBridge) executeTool(ctx context.Context, sessionKey string, tc providers.ToolCall) *tools.ToolResult {
	if ab.tools == nil {
		logger.WarnCF("livekit", "Tool call blocked: no tool registry available", map[string]any{
			"tool":         tc.Name,
			"tool_call_id": tc.ID,
			"session":      sessionKey,
		})
		return tools.ErrorResult("No tools available")
	}
	if !ab.isToolAllowed(tc.Name) {
		logger.WarnCF("livekit", "Tool call blocked by allowlist", map[string]any{
			"tool":         tc.Name,
			"tool_call_id": tc.ID,
			"session":      sessionKey,
		})
		return tools.ErrorResult(fmt.Sprintf("tool %q is not allowed in LiveKit voice runtime", tc.Name))
	}
	start := time.Now()
	logger.InfoCF("livekit", "Tool call started", map[string]any{
		"tool":            tc.Name,
		"tool_call_id":    tc.ID,
		"session":         sessionKey,
		"arguments":       toolArgsPreview(tc.Arguments, 240),
		"argument_fields": len(tc.Arguments),
	})

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

	result := ab.tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, "livekit", sessionKey, asyncCb)
	durationMS := time.Since(start).Milliseconds()
	if result == nil {
		logger.ErrorCF("livekit", "Tool call returned nil result", map[string]any{
			"tool":         tc.Name,
			"tool_call_id": tc.ID,
			"session":      sessionKey,
			"duration_ms":  durationMS,
		})
		return tools.ErrorResult(fmt.Sprintf("tool %q returned nil result", tc.Name))
	}
	logger.InfoCF("livekit", "Tool call finished", map[string]any{
		"tool":         tc.Name,
		"tool_call_id": tc.ID,
		"session":      sessionKey,
		"duration_ms":  durationMS,
		"is_error":     result.IsError,
		"llm_preview":  trimForLog(result.ContentForLLM(), 240),
	})
	ab.maybeMirrorWorkspaceArtifact(ctx, sessionKey, tc, result)
	return result
}

func normalizeAllowedToolNames(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		allowed[key] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

func (ab *AgentBridge) isToolAllowed(name string) bool {
	if ab == nil || len(ab.allowedToolNames) == 0 {
		return true
	}
	_, ok := ab.allowedToolNames[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func (ab *AgentBridge) filterToolDefsByAllowlist(defs []providers.ToolDefinition) []providers.ToolDefinition {
	if ab == nil || len(ab.allowedToolNames) == 0 || len(defs) == 0 {
		return defs
	}
	filtered := make([]providers.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		if _, ok := ab.allowedToolNames[strings.ToLower(strings.TrimSpace(def.Function.Name))]; ok {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func (ab *AgentBridge) maybeMirrorWorkspaceArtifact(ctx context.Context, sessionKey string, tc providers.ToolCall, result *tools.ToolResult) {
	if ab == nil || ab.workspaceArtifacts == nil || result == nil || result.IsError || tc.Name != "write_file" {
		return
	}
	workspace := ""
	if ab.agentInstance != nil {
		workspace = ab.agentInstance.Workspace
	}
	pathArg, _ := tc.Arguments["path"].(string)
	content, ok := tc.Arguments["content"].(string)
	if strings.TrimSpace(pathArg) == "" || !ok {
		return
	}
	rel, ok := artifactRelativePath(workspace, pathArg)
	if !ok {
		logger.WarnCF("livekit", "Skipping workspace artifact mirror: path is outside workspace", map[string]any{
			"path":    pathArg,
			"session": sessionKey,
		})
		return
	}
	if err := ab.workspaceArtifacts.SaveArtifact(ctx, WorkspaceArtifact{
		SessionID:    sessionKey,
		RelativePath: rel,
		Content:      content,
		ContentType:  "text/plain",
	}); err != nil {
		logger.WarnCF("livekit", "Failed to mirror workspace artifact", map[string]any{
			"path":    rel,
			"session": sessionKey,
			"error":   err.Error(),
		})
	}
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
	_, err := ab.runIterationWithProfile(ctx, sessionKey, messages, cb, onDone, "proactive")
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

	_, err := ab.runIterationWithProfile(ctx, sessionKey, messages, cb, onDone, "proactive")
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

// SessionQualitySnapshot returns aggregate runtime quality metrics.
func (ab *AgentBridge) SessionQualitySnapshot() SessionQualityReport {
	ab.qualityMu.Lock()
	defer ab.qualityMu.Unlock()

	report := SessionQualityReport{
		FallbackCount:         ab.quality.fallbackCount,
		InterruptionCount:     ab.quality.interruptionCount,
		InterruptionRecovered: ab.quality.interruptionRecovered,
		ErrorCount:            ab.quality.errorCount,
		RetryCount:            ab.quality.retryCount,
	}

	if report.InterruptionCount > 0 {
		report.InterruptionRecoveryRate = float64(report.InterruptionRecovered) / float64(report.InterruptionCount)
	}

	if len(ab.quality.ttftSamplesMS) == 0 {
		return report
	}

	samples := make([]int64, len(ab.quality.ttftSamplesMS))
	copy(samples, ab.quality.ttftSamplesMS)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	var total int64
	for _, v := range samples {
		total += v
	}
	report.AvgTTFTMs = total / int64(len(samples))

	mid := len(samples) / 2
	if len(samples)%2 == 0 {
		report.MedianTTFTMs = (samples[mid-1] + samples[mid]) / 2
	} else {
		report.MedianTTFTMs = samples[mid]
	}
	return report
}

// SessionTraceSnapshot returns a defensive copy of runtime event trace data.
func (ab *AgentBridge) SessionTraceSnapshot() []RuntimeEvent {
	ab.traceMu.Lock()
	defer ab.traceMu.Unlock()
	out := make([]RuntimeEvent, len(ab.traceLog))
	copy(out, ab.traceLog)
	return out
}
