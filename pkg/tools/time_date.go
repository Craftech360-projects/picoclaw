package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GetTimeDateTool returns deterministic wall-clock date/time information.
type GetTimeDateTool struct{}

func NewGetTimeDateTool() *GetTimeDateTool {
	return &GetTimeDateTool{}
}

func (t *GetTimeDateTool) Name() string {
	return "get_time_date"
}

func (t *GetTimeDateTool) Description() string {
	return "Get the current date and time, optionally for a specific IANA timezone like Asia/Kolkata or America/New_York."
}

func (t *GetTimeDateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "Optional IANA timezone, e.g. Asia/Kolkata.",
			},
		},
	}
}

func (t *GetTimeDateTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	tz := ""
	if raw, ok := args["timezone"]; ok {
		if v, ok := raw.(string); ok {
			tz = strings.TrimSpace(v)
		}
	}

	loc := time.Local
	locationName := loc.String()
	if tz != "" {
		loaded, err := time.LoadLocation(tz)
		if err != nil {
			return ErrorResult(fmt.Sprintf("invalid timezone %q: %v", tz, err))
		}
		loc = loaded
		locationName = tz
	}

	now := time.Now().In(loc)
	payload := map[string]any{
		"timestamp_unix": now.Unix(),
		"iso8601":        now.Format(time.RFC3339),
		"date":           now.Format("2006-01-02"),
		"time":           now.Format("15:04:05"),
		"weekday":        now.Weekday().String(),
		"timezone":       locationName,
		"utc_offset":     now.Format("-07:00"),
	}

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode time response: %v", err))
	}

	return &ToolResult{
		ForLLM: string(body),
		ForUser: fmt.Sprintf(
			"Current time in %s is %s (%s).",
			locationName,
			now.Format("2006-01-02 15:04:05"),
			now.Format("-07:00"),
		),
	}
}
