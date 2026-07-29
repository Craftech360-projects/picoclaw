package livekit

import (
	"strings"
	"testing"
)

func TestBuildGreetingInstruction(t *testing.T) {
	// Per-character prompt from ai_agent_template.greeting_prompt wins.
	got := buildGreetingInstruction("Tenali", "Open with a riddle about a king.")
	if !strings.Contains(got, "Open with a riddle about a king.") {
		t.Fatalf("custom greeting prompt not used: %q", got)
	}
	if strings.Contains(got, "using your persona guidelines") {
		t.Fatalf("generic instruction leaked into custom greeting: %q", got)
	}
	if !strings.Contains(got, "you are Tenali now") {
		t.Fatalf("character-switch note missing: %q", got)
	}

	// No template greeting -> today's name-aware default.
	got = buildGreetingInstruction("Tenali", "   ")
	if !strings.Contains(got, "Introduce yourself as Tenali") {
		t.Fatalf("name-aware default not used: %q", got)
	}

	// No character either -> generic default.
	got = buildGreetingInstruction("", "")
	if !strings.Contains(got, "Please proactively introduce yourself") {
		t.Fatalf("generic default not used: %q", got)
	}
}

// The greeting profile must keep the proactive token budget while being the only
// profile that drops tools: sending tools contradicts the persona prompt and makes
// tool-tuned models emit a JSON thought scaffold before the first spoken word.
func TestGreetingProfileSharesProactiveOptionsButDropsTools(t *testing.T) {
	ab := &AgentBridge{
		llmOptions:          map[string]any{"max_tokens": 120},
		proactiveLLMOptions: map[string]any{"max_tokens": 260},
	}

	if got := ab.optionsForProfile("greeting"); llmOptionValue(got, "max_tokens") != 260 {
		t.Errorf("greeting max_tokens = %v, want the proactive budget 260", llmOptionValue(got, "max_tokens"))
	}
	if got := ab.optionsForProfile("voice"); llmOptionValue(got, "max_tokens") != 120 {
		t.Errorf("non-proactive profile should keep its own budget, got %v", llmOptionValue(got, "max_tokens"))
	}

	// Only "greeting" drops tools; announcements still run on "proactive" with them.
	for profile, wantDropped := range map[string]bool{"greeting": true, "proactive": false, "voice": false} {
		dropped := strings.EqualFold(profile, "greeting")
		if dropped != wantDropped {
			t.Errorf("profile %q tool-drop = %v, want %v", profile, dropped, wantDropped)
		}
	}
}
