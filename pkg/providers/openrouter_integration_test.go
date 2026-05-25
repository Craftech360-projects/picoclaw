//go:build integration

package providers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegration_OpenRouterGemma4Chat(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set; skipping OpenRouter integration test")
	}

	model := strings.TrimSpace(os.Getenv("OPENROUTER_MODEL"))
	if model == "" {
		model = "google/gemma-4-31b-it"
	}

	provider := NewHTTPProvider(apiKey, "https://openrouter.ai/api/v1", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, []Message{
		{Role: "user", Content: "Reply with exactly READY"},
	}, nil, model, map[string]any{
		"temperature": 0,
		"max_tokens":  24,
	})
	if err != nil {
		t.Fatalf("OpenRouter chat failed: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		t.Fatalf("response content is empty: %+v", resp)
	}
	if !strings.Contains(strings.ToUpper(content), "READY") {
		t.Fatalf("unexpected response content: %q", content)
	}
}
