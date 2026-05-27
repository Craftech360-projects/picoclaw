package livekit

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAssistantMessageToolCallPreservesGeminiThoughtSignature(t *testing.T) {
	got := assistantMessageToolCall(providers.ToolCall{
		ID:   "call_1",
		Type: "function",
		Name: "get_weather",
		Arguments: map[string]any{
			"location": "Bangalore",
		},
		ThoughtSignature: "sig123",
		ExtraContent: &providers.ExtraContent{
			Google: &providers.GoogleExtra{
				ThoughtSignature: "sig123",
			},
		},
	})

	if got.ThoughtSignature != "sig123" {
		t.Fatalf("ThoughtSignature = %q, want sig123", got.ThoughtSignature)
	}
	if got.Function == nil {
		t.Fatal("Function is nil")
	}
	if got.Function.ThoughtSignature != "sig123" {
		t.Fatalf("Function.ThoughtSignature = %q, want sig123", got.Function.ThoughtSignature)
	}
	if got.ExtraContent == nil || got.ExtraContent.Google == nil {
		t.Fatal("ExtraContent.Google is nil")
	}
	if got.ExtraContent.Google.ThoughtSignature != "sig123" {
		t.Fatalf("ExtraContent.Google.ThoughtSignature = %q, want sig123", got.ExtraContent.Google.ThoughtSignature)
	}
}

func TestAssistantMessageToolCallUsesGoogleExtraThoughtSignature(t *testing.T) {
	got := assistantMessageToolCall(providers.ToolCall{
		ID:        "call_1",
		Type:      "function",
		Name:      "get_weather",
		Arguments: map[string]any{},
		ExtraContent: &providers.ExtraContent{
			Google: &providers.GoogleExtra{
				ThoughtSignature: "sig-from-extra",
			},
		},
	})

	if got.ThoughtSignature != "sig-from-extra" {
		t.Fatalf("ThoughtSignature = %q, want sig-from-extra", got.ThoughtSignature)
	}
	if got.Function == nil {
		t.Fatal("Function is nil")
	}
	if got.Function.ThoughtSignature != "sig-from-extra" {
		t.Fatalf("Function.ThoughtSignature = %q, want sig-from-extra", got.Function.ThoughtSignature)
	}
}
