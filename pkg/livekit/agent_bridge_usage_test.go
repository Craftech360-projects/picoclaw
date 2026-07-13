package livekit

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// TestRecordUsageAccumulatesCachedTokens verifies that cached prompt tokens
// reported by the provider (usage.prompt_tokens_details.cached_tokens) are
// summed across turns and surfaced in UsageSnapshot — previously hardcoded 0.
func TestRecordUsageAccumulatesCachedTokens(t *testing.T) {
	ab := &AgentBridge{}

	ab.recordUsage(&providers.UsageInfo{
		PromptTokens:        1000,
		CompletionTokens:    50,
		PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 800},
	}, 10*time.Millisecond)

	// Second turn, no details block (older/other provider) — cached stays put.
	ab.recordUsage(&providers.UsageInfo{
		PromptTokens:     500,
		CompletionTokens: 20,
	}, 10*time.Millisecond)

	snap := ab.UsageSnapshot()
	if snap.InputTokens != 1500 {
		t.Errorf("InputTokens = %d, want 1500", snap.InputTokens)
	}
	if snap.InputCachedTokens != 800 {
		t.Errorf("InputCachedTokens = %d, want 800 (was hardcoded 0 before fix)", snap.InputCachedTokens)
	}
	if snap.OutputTokens != 70 {
		t.Errorf("OutputTokens = %d, want 70", snap.OutputTokens)
	}
}
