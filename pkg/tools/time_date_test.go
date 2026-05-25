package tools

import (
	"context"
	"strings"
	"testing"
)

func TestGetTimeDateToolReturnsTimezonePayload(t *testing.T) {
	tool := NewGetTimeDateTool()
	result := tool.Execute(context.Background(), map[string]any{
		"timezone": "Asia/Kolkata",
	})
	if result == nil || result.IsError {
		t.Fatalf("result = %#v, want success", result)
	}
	if !strings.Contains(result.ForLLM, "\"timezone\": \"Asia/Kolkata\"") {
		t.Fatalf("expected timezone in payload, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "\"iso8601\"") {
		t.Fatalf("expected iso8601 in payload, got: %s", result.ForLLM)
	}
}

func TestGetTimeDateToolRejectsInvalidTimezone(t *testing.T) {
	tool := NewGetTimeDateTool()
	result := tool.Execute(context.Background(), map[string]any{
		"timezone": "Invalid/Timezone",
	})
	if result == nil || !result.IsError {
		t.Fatalf("result = %#v, want error", result)
	}
	if !strings.Contains(strings.ToLower(result.ForLLM), "invalid timezone") {
		t.Fatalf("unexpected error message: %s", result.ForLLM)
	}
}
