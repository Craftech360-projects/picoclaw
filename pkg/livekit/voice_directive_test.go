package livekit

import (
	"strings"
	"testing"
)

func TestVoiceModeDirective_KeepsFunctionalInvariants(t *testing.T) {
	d := voiceModeDirective()

	// All 14 Cheeko Face tags MUST survive verbatim — the frontend parses them.
	tags := []string{
		"[neutral]", "[happy]", "[excited]", "[laughing]", "[love]", "[silly]",
		"[curious]", "[surprised]", "[confused]", "[shy]", "[sad]", "[crying]",
		"[angry]", "[scared]", "[sleepy]",
	}
	for _, tag := range tags {
		if !strings.Contains(d, tag) {
			t.Errorf("directive dropped required face tag %q", tag)
		}
	}

	// Essential behavioral constraints must remain.
	for _, must := range []string{"1-3 sentences", "No markdown", "get_weather", "get_time_date", "web_search", "USER.md", "MEMORY.md"} {
		if !strings.Contains(d, must) {
			t.Errorf("directive dropped essential instruction %q", must)
		}
	}

	// Budget guard: this is per-turn overhead. Fail if it creeps back up.
	// ~4 chars/token; 1600 chars ~= 400 tokens ceiling.
	if len(d) > 1600 {
		t.Errorf("voice directive grew to %d chars (>1600); it is re-sent every turn", len(d))
	}
}
