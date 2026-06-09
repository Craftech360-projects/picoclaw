package livekit

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type toolCallProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *toolCallProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				ID:        "call-1",
				Name:      "blocking_tool",
				Arguments: map[string]any{},
			}},
		}, nil
	}
	return &providers.LLMResponse{Content: "done"}, nil
}

func (p *toolCallProvider) GetDefaultModel() string { return "test" }

type cancelAwareTool struct {
	started  chan struct{}
	canceled chan struct{}
}

func (t *cancelAwareTool) Name() string        { return "blocking_tool" }
func (t *cancelAwareTool) Description() string { return "blocks until context cancellation" }
func (t *cancelAwareTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *cancelAwareTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	closeOnce(t.started)
	select {
	case <-ctx.Done():
		closeOnce(t.canceled)
		return tools.ErrorResult(ctx.Err().Error()).WithError(ctx.Err())
	case <-time.After(500 * time.Millisecond):
		return tools.NewToolResult("context was not canceled")
	}
}

type nonCooperativeBlockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *nonCooperativeBlockingTool) Name() string        { return "blocking_tool" }
func (t *nonCooperativeBlockingTool) Description() string { return "ignores context cancellation" }
func (t *nonCooperativeBlockingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *nonCooperativeBlockingTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	closeOnce(t.started)
	<-t.release
	return tools.NewToolResult("late success")
}

func TestAsyncToolExecutionUsesCancelableTurnContext(t *testing.T) {
	registry := tools.NewToolRegistry()
	probe := &cancelAwareTool{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	registry.Register(probe)
	provider := &toolCallProvider{}
	bridge := &AgentBridge{
		provider:             provider,
		tools:                registry,
		llmOptions:           map[string]any{},
		proactiveLLMOptions:  map[string]any{},
		asyncEventChan:       make(chan AsyncEvent, 1),
		runtimeEventChan:     make(chan RuntimeEvent, 8),
		toolExecutionTimeout: time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	asyncPending, err := bridge.runIterationWithProfile(
		ctx,
		"session-1",
		[]providers.Message{{Role: "user", Content: "use tool"}},
		nil,
		nil,
		"conversation",
	)
	if err != nil {
		t.Fatalf("runIterationWithProfile returned error: %v", err)
	}
	if !asyncPending {
		t.Fatal("runIterationWithProfile asyncPending = false, want true")
	}
	waitForSignal(t, probe.started, "tool start")
	cancel()
	waitForSignal(t, probe.canceled, "tool context cancellation")
}

func TestExecuteToolReturnsTimeoutWhenToolDoesNotCooperate(t *testing.T) {
	registry := tools.NewToolRegistry()
	blocking := &nonCooperativeBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry.Register(blocking)
	defer close(blocking.release)
	bridge := &AgentBridge{
		tools:                registry,
		toolExecutionTimeout: 25 * time.Millisecond,
	}

	startedAt := time.Now()
	result := bridge.executeTool(context.Background(), "session-1", providers.ToolCall{
		Name:      "blocking_tool",
		Arguments: map[string]any{},
	})
	elapsed := time.Since(startedAt)

	waitForSignal(t, blocking.started, "tool start")
	if elapsed > 250*time.Millisecond {
		t.Fatalf("executeTool took %s, want strict timeout return", elapsed)
	}
	if result == nil || !result.IsError {
		t.Fatalf("result = %#v, want timeout error", result)
	}
	if !strings.Contains(strings.ToLower(result.ForLLM), "timed out") {
		t.Fatalf("timeout result = %q, want timed out", result.ForLLM)
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}
