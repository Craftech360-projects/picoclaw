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
	Greeting string // full greeting instruction; also folded into Voice's <greeting_guidance> section
}

// gptLiveAccentIndian is a modifier on HOW to speak English, not a competing language
// instruction: it must never read as an imperative to speak English, because it can be
// combined with a language lock (gptLiveDelegationBlock) that names a different language,
// e.g. Hindi. The two must not contradict each other in the composed Voice string.
const gptLiveAccentIndian = `
<accent>
When you speak English, use an Indian accent: Indian intonation and rhythm, and the everyday
phrasing a child in India hears at home and at school. Keep it natural and warm, never a caricature.
This is about how to sound in English, not which language to use — follow the language
instruction above for that.
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

// gptLiveGreetingGuidance renders, for the Voice channel, what the model should say the
// first time it is asked to greet the child. Session-start instructions (Voice) carry no
// length cap, unlike a session.commentary.append (capped at 500 tokens by the service —
// see the "gptlive: session error ... Context append text must not exceed 500 tokens"
// production log this fixes). A manager-supplied greeting_prompt can run well past that
// cap, so the full guidance belongs here; Greet (gptlive_pipeline.go) then only ever sends
// a short nudge that points back at this section instead of repeating the guidance itself.
func gptLiveGreetingGuidance(characterName, greetingPrompt string) string {
	return "<greeting_guidance>\n" +
		"The first time you are asked to greet the child, open the conversation using this guidance:\n\n" +
		buildGreetingInstruction(characterName, greetingPrompt) +
		"\n</greeting_guidance>"
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
	parts = append(parts, gptLiveGreetingGuidance(in.CharacterName, in.GreetingPrompt))
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
