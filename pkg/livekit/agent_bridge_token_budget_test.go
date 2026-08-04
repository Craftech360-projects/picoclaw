package livekit

import "testing"

// Regression (live 2026-08-04): greetings were capped at the raw voice budget
// (120 tokens ~ 480 chars) and cut off mid-sentence before the question and the
// state MEMO. main.go always seeds max_tokens, so a default-if-absent guard
// never fired — the floor must override a lower caller value.
func TestBuildProactiveLLMOptionsFloorsMaxTokens(t *testing.T) {
	out := buildProactiveLLMOptions(map[string]any{"max_tokens": 120, "temperature": 0.7})
	if got := optionInt(out["max_tokens"]); got < proactiveMinTokens {
		t.Fatalf("seeded low cap not raised: max_tokens=%d, want >=%d", got, proactiveMinTokens)
	}
	if out["temperature"] != 0.7 {
		t.Fatalf("caller temperature overwritten: %v", out["temperature"])
	}

	// an already-generous cap is left alone
	if got := optionInt(buildProactiveLLMOptions(map[string]any{"max_tokens": 900})["max_tokens"]); got != 900 {
		t.Fatalf("generous cap lowered to %d", got)
	}

	// absent -> floor applied
	if got := optionInt(buildProactiveLLMOptions(map[string]any{})["max_tokens"]); got < proactiveMinTokens {
		t.Fatalf("absent cap not floored: %d", got)
	}
}

// Conversation turns carry tools; the budget must fit a story beat + MEMO.
func TestEnsureToolCallTokenBudgetFitsStateMemo(t *testing.T) {
	opts := map[string]any{"max_tokens": 120}
	ensureToolCallTokenBudget("google/gemma-4-31b-it", opts, 3)
	if got := optionInt(opts["max_tokens"]); got < 420 {
		t.Fatalf("conversation budget too small for beat+memo: %d", got)
	}
}
