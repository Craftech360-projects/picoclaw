package main

import "testing"

// Both directions of this gate fail silently: a toolless character keeping its
// tools looks like a normal session, and an unknown character losing them is an
// invisible capability regression nobody notices until a child asks for the
// weather.
func TestLiveKitCharacterToolGate(t *testing.T) {
	tests := []struct {
		character string
		wantTools bool
	}{
		{"quizzy", false},
		{"Quizzy", false}, // character name arrives from room metadata, casing not guaranteed
		{" quizzy ", false},
		{"bujho", false},
		{"riddler", true}, // pre-2026-08-20 display name; renamed, so no longer matched
		{"cheeko", false}, // 2026-08-20 pack persona: "Never call tools"
		{"Tara", false},
		{"nani", false},
		{"tikku", false},
		{"Cheeko German", true}, // match is exact: a persona variant needs its own list entry
		{"", true},              // unknown character must keep tools, never silently lose them
		{"quizmaster", true},    // near-miss must not match
		{"math tutor", true},    // future specialized character defaults to tools
	}

	for _, tt := range tests {
		if got := liveKitCharacterUsesTools(tt.character); got != tt.wantTools {
			t.Errorf("liveKitCharacterUsesTools(%q) = %v, want %v", tt.character, got, tt.wantTools)
		}
	}
}

// A toolless character must be denied at BOTH registration paths. The forced
// path exists to rescue a misconfigured agent, and if it is not gated too it
// hands back every tool the allowlist just refused.
func TestToollessCharacterDeniedOnBothPaths(t *testing.T) {
	for _, name := range liveKitVoiceAllowedTools {
		if isLiveKitVoiceAllowedToolFor("quizzy", name) {
			t.Errorf("quizzy was allowed tool %q via the allowlist path", name)
		}
		if !isLiveKitVoiceAllowedToolFor("quizmaster", name) {
			t.Errorf("unknown character was denied tool %q it should keep", name)
		}
	}

	// nil instance is safe here only because a toolless character returns before
	// touching it - which is itself the behaviour under test.
	if added := ensureLiveKitVoiceToolsFor("quizzy", nil, nil, nil); added != nil {
		t.Errorf("forced-tool path registered %v for quizzy, want none", added)
	}
}
