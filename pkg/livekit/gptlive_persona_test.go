package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGPTLivePersonaComposesWorkspaceAndRules(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "AGENT.md"), []byte("You are Quizzy, the quiz master."), 0o644)
	os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("Warm, curious, never sarcastic."), 0o644)
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Quizzy", GreetingPrompt: "Start with the first question.",
		LanguageName: "Hindi", Accent: "indian", HasQuiz: true,
		BankBlock: "## Today's Quiz Questions (Level 1, band 6-8)\n3. (id=11) How many legs does a spider have? — Answer: eight",
	})
	for _, want := range []string{"You are Quizzy", "Warm, curious", "<delegation>", "Hindi", "<accent>", "(id=11)", "quiz_score_answer"} {
		if !strings.Contains(p.Voice, want) {
			t.Errorf("voice instructions missing %q", want)
		}
	}
	// The language lock (gptLiveDelegationBlock) and the accent block (gptLiveAccentIndian)
	// land in the same Voice string and must not contradict each other: with LanguageName
	// "Hindi" and Accent "indian", the accent guidance must read as "when you do speak
	// English, use an Indian accent" — a modifier — never as a bare command to speak
	// English, which would fight the Hindi language lock.
	if !strings.Contains(p.Voice, "Speak Hindi") {
		t.Error("language lock must tell the model to speak Hindi")
	}
	if strings.Contains(p.Voice, "Speak English") {
		t.Error("accent guidance must not command speaking English when the language lock says Hindi")
	}
	if !strings.Contains(p.Voice, "use an Indian accent") {
		t.Error("accent block should describe how to sound, not compete with the language lock")
	}
	for _, want := range []string{"(id=11)", "quiz_score_answer", "child"} {
		if !strings.Contains(p.Backend, want) {
			t.Errorf("backend instructions missing %q", want)
		}
	}
	if !strings.Contains(p.Greeting, "Start with the first question.") || !strings.Contains(p.Greeting, "Quizzy") {
		t.Errorf("greeting: %q", p.Greeting)
	}
	q := BuildGPTLivePersona(GPTLivePersonaInput{Workspace: ws, CharacterName: "Cheeko", Accent: "default"})
	if strings.Contains(q.Voice, "quiz_score_answer") {
		t.Error("no quiz rules for a plain character with HasQuiz false")
	}
}

// TestBuildGPTLivePersonaEmptyWorkspace pins the behavior for a workspace directory that
// exists but has none of AGENT.md/SOUL.md/USER.md yet (a brand-new device folder). The
// composer must not panic or fail, and the voice instructions fall back to the generic
// Cheeko identity baked into agent.BuildSystemPrompt.
func TestBuildGPTLivePersonaEmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Cheeko",
	})
	if !strings.Contains(p.Voice, "Cheeko") {
		t.Errorf("empty workspace should still fall back to the default Cheeko identity, got %q", p.Voice)
	}
	if !strings.Contains(p.Voice, "<delegation>") {
		t.Error("delegation block must always be present even with an empty workspace")
	}
	if strings.Contains(p.Voice, "<accent>") {
		t.Error("no accent requested, so no accent block")
	}
	if strings.Contains(p.Backend, "quiz_score_answer") {
		t.Error("HasQuiz is false, backend must not mention quiz scoring")
	}
}

// TestBuildGPTLivePersonaAccentOnlyIndianIsSpecialCased pins the contract that "indian" is
// the only recognized accent value. An empty string (no accent configured at all), the
// literal "default", and any unrecognized value must all behave the same way: no <accent>
// block appended, and no silent coercion into some other accent.
func TestBuildGPTLivePersonaAccentOnlyIndianIsSpecialCased(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		name   string
		accent string
	}{
		{"empty (not configured)", ""},
		{"literal default", "default"},
		{"unrecognized value", "british"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildGPTLivePersona(GPTLivePersonaInput{Workspace: ws, CharacterName: "Cheeko", Accent: tc.accent})
			if strings.Contains(p.Voice, "<accent>") {
				t.Errorf("Accent %q must not add an accent block, got Voice=%q", tc.accent, p.Voice)
			}
		})
	}
}

// TestBuildGPTLivePersonaAbsentBankBlock pins BankBlock == "" (character has a quiz but no
// question was rendered for this turn, or the character has no bank at all): neither Voice
// nor Backend should gain an empty extra section, and no id markers leak in from other tests.
func TestBuildGPTLivePersonaAbsentBankBlock(t *testing.T) {
	ws := t.TempDir()
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Quizzy", HasQuiz: true, BankBlock: "",
	})
	if strings.Contains(p.Voice, "Today's Quiz Questions") {
		t.Error("absent bank block must not appear in the voice instructions")
	}
	if strings.Contains(p.Backend, "Today's Quiz Questions") {
		t.Error("absent bank block must not appear in the backend instructions")
	}
	if !strings.Contains(p.Backend, "quiz_score_answer") {
		t.Error("HasQuiz is true, so backend must still carry the scoring rule even without a bank block")
	}
}

// TestBuildGPTLivePersonaVoiceCarriesFullGreetingGuidance covers the production fix: a real
// session logged "gptlive: invalid_request_error (invalid_value): Context append text must
// not exceed 500 tokens" because Greet used to append the full manager-supplied
// greeting_prompt as commentary, a channel the service caps per-append — a 2455-byte prompt
// blew straight through it and the child was never greeted. Session-start instructions
// (Voice) carry no such cap, so the greeting guidance must appear there in full, however
// long the prompt is; this reproduces a prompt sized like the one that failed in production.
func TestBuildGPTLivePersonaVoiceCarriesFullGreetingGuidance(t *testing.T) {
	ws := t.TempDir()
	longPrompt := strings.Repeat("Ask the child about their day at school and what they had for lunch. ", 40)
	if len(longPrompt) < 2000 {
		t.Fatalf("test fixture too small to stand in for the 2455-byte production prompt: %d bytes", len(longPrompt))
	}
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Tenali", GreetingPrompt: longPrompt,
	})
	if !strings.Contains(p.Voice, "<greeting_guidance>") {
		t.Error("Voice must carry a labelled greeting_guidance section")
	}
	if !strings.Contains(p.Voice, longPrompt) {
		t.Error("Voice must carry the full greeting prompt verbatim, uncapped, unlike a context append")
	}
}

// TestBuildGPTLivePersonaEmptyGreetingPrompt pins the fallback greeting used when the
// character has no configured greeting_prompt: buildGreetingInstruction's generic
// "introduce yourself" text, not an empty or malformed string.
func TestBuildGPTLivePersonaEmptyGreetingPrompt(t *testing.T) {
	ws := t.TempDir()
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Cheeko", GreetingPrompt: "",
	})
	want := "[System Event] The user has successfully connected to the room and is now listening. You are Cheeko. Introduce yourself as Cheeko and greet them using your persona guidelines. Earlier conversation may have been with a different character; you are Cheeko now."
	if p.Greeting != want {
		t.Errorf("empty greeting prompt:\n got %q\nwant %q", p.Greeting, want)
	}
}
