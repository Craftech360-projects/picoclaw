package openai_compat

import (
	"os"
	"strings"
	"testing"
)

// TestLiveOpenRouterUsageAndCaching hits the real OpenRouter endpoint to prove
// the streaming path now returns usage (PromptTokens) and, on a cache-capable
// model, cached_tokens on the second identical request. Gated on
// OPENROUTER_API_KEY so it never runs in CI without a key.
//
// Run: OPENROUTER_API_KEY=... go test ./pkg/providers/openai_compat/ \
//        -run TestLiveOpenRouterUsageAndCaching -v -count=1
func TestLiveOpenRouterUsageAndCaching(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live OpenRouter test")
	}

	model := os.Getenv("OPENROUTER_TEST_MODEL")
	if model == "" {
		model = "openai/gpt-4.1-mini" // cache-capable + cheap
	}

	p := NewProvider(key, "https://openrouter.ai/api/v1", "")

	// A large (>1024 token) identical prefix so OpenAI-family implicit caching
	// can trigger on the second call. Repeat a filler paragraph.
	filler := strings.Repeat(
		"You are a helpful assistant for a children's voice toy named Cheeko. "+
			"Keep replies short, warm, and age-appropriate. ", 120)
	messages := []Message{
		{Role: "system", Content: filler},
		{Role: "user", Content: "Reply with exactly: OK"},
	}
	opts := map[string]any{"max_tokens": 50}

	call := func(label string) *LLMResponse {
		resp, err := p.ChatStream(t.Context(), messages, nil, model, opts, nil)
		if err != nil {
			t.Fatalf("%s: ChatStream error: %v", label, err)
		}
		if resp.Usage == nil {
			t.Fatalf("%s: resp.Usage is nil — streaming usage NOT returned (include_usage missing?)", label)
		}
		cached := 0
		if resp.Usage.PromptTokensDetails != nil {
			cached = resp.Usage.PromptTokensDetails.CachedTokens
		}
		t.Logf("%s [%s]: prompt=%d completion=%d cached=%d content=%q",
			label, model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cached, resp.Content)
		return resp
	}

	r1 := call("call#1 (cache write)")
	if r1.Usage.PromptTokens <= 0 {
		t.Fatalf("PromptTokens=%d, want >0 — input tokens not measured on streaming path", r1.Usage.PromptTokens)
	}

	r2 := call("call#2 (cache read)")
	if r2.Usage.PromptTokens <= 0 {
		t.Fatalf("call#2 PromptTokens=%d, want >0", r2.Usage.PromptTokens)
	}

	// Informational: cached>0 proves the cache path + parsing end-to-end, but
	// caching is best-effort (provider routing, warmup), so don't hard-fail.
	cached2 := 0
	if r2.Usage.PromptTokensDetails != nil {
		cached2 = r2.Usage.PromptTokensDetails.CachedTokens
	}
	if cached2 > 0 {
		t.Logf("CACHE CONFIRMED: call#2 read %d cached tokens (%.0f%% of prompt)",
			cached2, 100*float64(cached2)/float64(r2.Usage.PromptTokens))
	} else {
		t.Logf("NOTE: cached=0 on call#2 — model/provider may not have cached (open model, cold cache, or routing). PromptTokens still measured correctly.")
	}
}
