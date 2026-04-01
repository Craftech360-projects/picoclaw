package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TimerTool allows the agent to set a timer and be asynchronously notified
// when it expires, which is particularly useful for voice agents.
type TimerTool struct{}

// NewTimerTool creates a new timer tool instance.
func NewTimerTool() *TimerTool {
	return &TimerTool{}
}

func (t *TimerTool) Name() string {
	return "create_timer"
}

func (t *TimerTool) Description() string {
	return "Sets a timer for a given number of seconds. When the timer finishes, you will be notified so you can verbally remind the user."
}

func (t *TimerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"duration_seconds": map[string]any{
				"type":        "integer",
				"description": "The duration of the timer in seconds",
			},
		},
		"required": []string{"duration_seconds"},
	}
}

func (t *TimerTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	// Fallback synchronous execution if not called via ExecuteAsync
	return AsyncResult("Timer tool requires async execution context")
}

// ExecuteAsync implements AsyncExecutor, allowing the timer to sleep in the
// background and trigger a callback (e.g., spontaneous voice speech) upon completion.
func (t *TimerTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
	var durationSecs int
	switch v := args["duration_seconds"].(type) {
	case float64:
		durationSecs = int(v)
	case int:
		durationSecs = v
	case int64:
		durationSecs = int(v)
	case json.Number:
		if fFloat, err := v.Float64(); err == nil {
			durationSecs = int(fFloat)
		} else {
			return ErrorResult("duration_seconds must be a valid number string")
		}
	default:
		// Attempt to parse string representation if provider used json.Number
		return ErrorResult(fmt.Sprintf("duration_seconds must be a number, got %T", args["duration_seconds"]))
	}

	if durationSecs <= 0 {
		return ErrorResult("duration_seconds must be greater than 0")
	}

	// Calculate target time to inform the LLM when it was effectively set for
	target := time.Now().Add(time.Duration(durationSecs) * time.Second)

	go func() {
		select {
		case <-time.After(time.Duration(durationSecs) * time.Second):
			if cb != nil {
				// The async completion message that the LLM will see when the timer fires
				cb(context.Background(), NewToolResult(fmt.Sprintf("The %d second timer you set has finished!", durationSecs)))
			}
		case <-ctx.Done():
			// The agent session or program exited before the timer finished
			return
		}
	}()

	return AsyncResult(fmt.Sprintf("Timer started for %d seconds. It will finish at %s.", durationSecs, target.Format(time.Kitchen)))
}
