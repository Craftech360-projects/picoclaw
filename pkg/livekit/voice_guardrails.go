package livekit

import (
	"regexp"
	"strings"
)

// Deterministic kid-safety responses that must never be left to model improvisation.
// Small local models (Gemma 4B, phi4-mini, etc.) do not reliably follow the self-harm
// script or the identity guard in the system prompt, so these cases are intercepted in
// code before the LLM is ever called. Wording mirrors AGENT.md / SOUL.md exactly.
const (
	selfHarmSafetyResponse  = "I'm really sorry you're feeling this way. Please talk to your parent or another trusted adult right now; they can help you."
	creatorIdentityResponse = "I was created by ALTIO AI Private Limited, just for being Cheeko."
)

// selfHarmRE matches a child expressing intent to harm themselves.
// ponytail: keyword heuristic, not a classifier. Over-triggering is the safe failure
// mode here (worst case: a calm adult-style redirect). Upgrade to a real safety
// classifier if false positives on phrases like "die laughing" become a problem.
var selfHarmRE = regexp.MustCompile(`(?i)\b(kill myself|hurt myself|hurting myself|harm myself|harming myself|end my life|want to die|wanna die|suicide|suicidal|cut myself|cutting myself|don'?t want to live|no reason to live)\b`)

// modelProbeRE matches the child asking which AI/model/company is behind Cheeko —
// the vector that makes small models blurt "I'm Gemma by Google" and break the identity guard.
var modelProbeRE = regexp.MustCompile(`(?i)\b(are you|are u|r u|you are|you're)\b.{0,40}\b(chatgpt|gpt|openai|gemini|gemma|google|qwen|alibaba|claude|anthropic|llama|meta ai|phi|microsoft|deepseek|mistral|language model|llm|a\.?i\.? model)\b`)

// voiceSafetyOverride returns a canned response and true when the child's message matches
// a case that must be handled deterministically rather than by the LLM. Returns "", false otherwise.
func voiceSafetyOverride(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", false
	}
	if selfHarmRE.MatchString(t) {
		return selfHarmSafetyResponse, true
	}
	if modelProbeRE.MatchString(t) {
		return creatorIdentityResponse, true
	}
	return "", false
}
