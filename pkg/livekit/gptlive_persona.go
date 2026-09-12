package livekit

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
)

// GPTLivePersonaInput carries everything needed to compose a GPT-Live persona once,
// at session start. Persona and voice are immutable for the life of the session: the
// cascade's per-turn system messages (voice directive, language lock, quiz Door
// directive) have no equivalent under GPT-Live, so their content is folded into the
// delegation block composed here instead.
type GPTLivePersonaInput struct {
	Workspace      string
	CharacterName  string
	GreetingPrompt string
	LanguageName   string // "English" when empty
	Accent         string // "indian" | "default" (anything else behaves like "default")
	BankBlock      string // RenderQuizQuestions / RenderContentBank output, may be empty
	HasQuiz        bool
}

// GPTLivePersona is everything that is immutable once the session starts.
type GPTLivePersona struct {
	Voice    string // the voice model's instructions
	Backend  string // the backend Responses model's instructions
	Greeting string // commentary sent when the device is ready
}

const gptLiveAccentIndian = `
<accent>
Speak Indian English: an Indian accent with Indian intonation and rhythm, and the everyday
phrasing a child in India hears at home and at school. Keep it natural and warm, never a caricature.
</accent>`

// gptLiveDelegationBlock is the per-session equivalent of the cascade's per-turn voice
// directive, language lock and quiz Door directive: since GPT-Live cannot append fresh
// system messages mid-session, all three are folded into one block composed once here.
func gptLiveDelegationBlock(language string, hasQuiz bool) string {
	if strings.TrimSpace(language) == "" {
		language = "English"
	}
	var b strings.Builder
	b.WriteString("<delegation>\n")
	b.WriteString("You cannot look things up or keep score yourself. Delegate any question about the current time,\n")
	b.WriteString("date or weather, any factual question you are not sure about, and anything worth remembering\n")
	b.WriteString("about the child. While you wait, say one short cheerful line, then read out the result when it arrives.\n")
	b.WriteString("Answer greetings, small talk, jokes and simple questions yourself.\n")
	if hasQuiz {
		b.WriteString("Every time the child answers a quiz question, delegate so the answer gets scored\n")
		b.WriteString("(the helper calls quiz_score_answer), and then do exactly what the result tells you to do next:\n")
		b.WriteString("ask plainly, offer the two choices, explain then re-ask, or reveal and move on.\n")
		b.WriteString("Never decide on your own whether an answer was right.\n")
	}
	fmt.Fprintf(&b, "Speak %s with the child unless they clearly switch language.\n", language)
	b.WriteString("</delegation>")
	return b.String()
}

// BuildGPTLivePersona composes the voice instructions, backend instructions and greeting
// commentary for a GPT-Live session. It is called once at session start; nothing it
// produces changes for the rest of the session.
func BuildGPTLivePersona(in GPTLivePersonaInput) GPTLivePersona {
	base := agent.NewContextBuilder(in.Workspace).BuildSystemPrompt()
	parts := []string{base, gptLiveDelegationBlock(in.LanguageName, in.HasQuiz)}
	if strings.TrimSpace(in.BankBlock) != "" {
		parts = append(parts, in.BankBlock)
	}
	if in.Accent == "indian" {
		parts = append(parts, strings.TrimSpace(gptLiveAccentIndian))
	}
	backend := "You handle the work a voice model delegates while it talks to a child aged 3 to 16. " +
		"Use tools when current information is required or when the child says something worth remembering. " +
		"Reply with one or two short, friendly, child-safe sentences the voice model can read out."
	if in.HasQuiz {
		backend += " For quiz answers, compare the child's words with the bank answer and accepted answers, " +
			"then call quiz_score_answer with result=correct or result=miss; call quiz_status if unsure which question is pending."
	}
	if strings.TrimSpace(in.BankBlock) != "" {
		backend += "\n\n" + in.BankBlock
	}
	return GPTLivePersona{
		Voice:    strings.Join(parts, "\n\n---\n\n"),
		Backend:  backend,
		Greeting: buildGreetingInstruction(in.CharacterName, in.GreetingPrompt),
	}
}
