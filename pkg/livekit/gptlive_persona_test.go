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

// TestStripGPTLiveMachineLines pins the pure-function behavior stripGPTLiveMachineLines
// relies on: MEMO: and QUIZ_BANK: header lines are removed line-anchored and
// case-insensitively (mirroring voiceMemoLineRE), everything else survives, and a run of
// blank lines the removal leaves behind collapses to a single blank line.
func TestStripGPTLiveMachineLines(t *testing.T) {
	in := "## Heading\n" +
		"quiz_bank: type=quiz_bank | date=2026-08-20\n\n" +
		"Some human text.\n" +
		"  MEMO: type=companion | date=2025-03-08 | parent_summary=hi\n" +
		"More human text that must survive."
	out := stripGPTLiveMachineLines(in)
	if strings.Contains(strings.ToLower(out), "memo:") || strings.Contains(strings.ToLower(out), "quiz_bank:") {
		t.Errorf("machine header lines must be gone, got %q", out)
	}
	for _, want := range []string{"## Heading", "Some human text.", "More human text that must survive."} {
		if !strings.Contains(out, want) {
			t.Errorf("stripping must not remove %q, got %q", want, out)
		}
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("blank-line runs left by removed headers must collapse, got %q", out)
	}
}

// TestBuildGPTLivePersonaStripsMemoLineFromVoice reproduces the production bug: a
// memory/state/*.md file begins with a machine-readable "MEMO: ..." header line (see
// pkg/agent/memory.go's ReadStateFiles, which labels these files "small runtime-managed
// files ... written by the voice worker", and quiz_state.go's per-type state files), and
// that header text reached Persona.Voice verbatim and got spoken to the child:
// "MEMO: type=companion | date=2025-03-08 | topics=greeting and asked about his day | ...
// parent_summary=Cheeko greeted Hitansh [happy". Under the cascade, voiceMemoLineRE
// (audio_pipeline.go) strips a MEMO line from the model's TEXT before TTS ever speaks it;
// GPT-Live's voice model speaks Voice directly with no text stage, so nothing filtered it.
// BankBlock is the channel real GPT-Live sessions use to carry saved/rendered state-shaped
// text into Voice (see cmd/picoclaw-livekit/main.go's bankBlockForSession), so this test
// feeds it a header in the exact shape a state file uses, verbatim from the production log.
func TestBuildGPTLivePersonaStripsMemoLineFromVoice(t *testing.T) {
	ws := t.TempDir()
	memoLine := "MEMO: type=companion | date=2025-03-08 | topics=greeting and asked about his day | " +
		"feeling=NONE | promised=NONE | handoff=NONE | parent_summary=Cheeko greeted Hitansh [happy"
	before := "Some saved detail before."
	after := "Some other saved note the character should still see."
	// The header sits mid-block, preceded and followed by prose - the shape
	// pkg/agent/memory.go's ReadStateFiles produces (a "### <file>.md" label, then the
	// file's own header line, then human text). This also exercises requirement 5: a plain
	// deletion of just the header's own text leaves two blank lines (four newlines) behind,
	// which must collapse to one.
	block := before + "\n\n" + memoLine + "\n\n" + after
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Cheeko",
		BankBlock: block,
	})
	if strings.Contains(p.Voice, "MEMO:") {
		t.Errorf("Voice must not contain a MEMO: line, got %q", p.Voice)
	}
	start, end := strings.Index(p.Voice, before), strings.Index(p.Voice, after)
	if start == -1 || end == -1 {
		t.Fatalf("stripping the MEMO header must not remove the rest of the block, got %q", p.Voice)
	}
	if section := p.Voice[start : end+len(after)]; strings.Contains(section, "\n\n\n") {
		t.Errorf("stripping the header must collapse the blank-line run it leaves behind, got %q", section)
	}
	// Requirement 3: the backend reasoning model may legitimately see this bookkeeping.
	if !strings.Contains(p.Backend, memoLine) {
		t.Errorf("Backend must keep the MEMO bookkeeping verbatim - stripping is voice-only, got %q", p.Backend)
	}
}

// TestBuildGPTLivePersonaStripsQuizBankHeaderKeepsWonderContent pins requirement 2 of the
// MEMO-leak fix: memory/state/quiz_bank.md (built by quiz_state.go's WriteQuizBankState)
// begins with a machine "QUIZ_BANK: ..." header line the child must never hear, immediately
// followed by human-readable sections - "Last Time You Wondered" and "Today's Wonder
// Question" - that the voice model genuinely needs to open the session correctly. Only the
// header line may go; getting this wrong (stripping too much) is exactly the failure mode
// the fix must avoid.
func TestBuildGPTLivePersonaStripsQuizBankHeaderKeepsWonderContent(t *testing.T) {
	ws := t.TempDir()
	wonderCallback := "Before the quiz, delight the child by remembering what THEY said. You asked them " +
		"\"Where does the wind start?\" and they answered \"From the trees\". Open with a warm callback."
	wonderQuestion := "When the session ends, leave the child with EXACTLY this question, in your own warm " +
		"words but the same question: \"What do fish dream about?\" (code WQ-FISH-01)."
	quizBankFile := "QUIZ_BANK: type=quiz_bank | date=2026-08-20 | bank=math | level=4 | band=all | replay=false\n\n" +
		"## Last Time You Wondered\n" + wonderCallback + "\n\n" +
		"## Today's Wonder Question\n" + wonderQuestion + "\n"
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Quizzy", HasQuiz: true,
		BankBlock: quizBankFile,
	})
	if strings.Contains(p.Voice, "QUIZ_BANK:") {
		t.Errorf("Voice must not contain a QUIZ_BANK: header line, got %q", p.Voice)
	}
	for _, want := range []string{"## Last Time You Wondered", wonderCallback, "## Today's Wonder Question", wonderQuestion} {
		if !strings.Contains(p.Voice, want) {
			t.Errorf("Voice must keep %q verbatim, got %q", want, p.Voice)
		}
	}
	start, end := strings.Index(p.Voice, "## Last Time You Wondered"), strings.Index(p.Voice, wonderQuestion)
	if start == -1 || end == -1 {
		t.Fatalf("expected both wonder sections in Voice, got %q", p.Voice)
	}
	if section := p.Voice[start : end+len(wonderQuestion)]; strings.Contains(section, "\n\n\n") {
		t.Errorf("stripping the header must not leave a run of blank lines, got %q", section)
	}
	// Requirement 3: the backend reasoning model may legitimately see this bookkeeping.
	if !strings.Contains(p.Backend, "QUIZ_BANK:") || !strings.Contains(p.Backend, quizBankFile) {
		t.Errorf("Backend must keep the full QUIZ_BANK block verbatim - stripping is voice-only, got %q", p.Backend)
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
