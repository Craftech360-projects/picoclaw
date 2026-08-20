package session

import (
	"regexp"
	"strings"
)

// Two machine channels travel inside a character's reply text: the
// square-bracket expression tags that drive the toy's face, and the hidden MEMO
// line that carries session state. Neither is ever spoken — TTS strips the tags
// and the MEMO is suppressed — so neither belongs in the stored conversation,
// where the parent app renders it as something the child was told.
//
// Kept here rather than in pkg/livekit because the store is what must be clean,
// and this package owns every write to it. Sanitizing at the persistence seam
// leaves the local history the model reads back untouched.

// Mirrors pkg/livekit's voiceExpressionTagRE. Duplicated deliberately: importing
// livekit from session would invert the dependency, and this pattern is stable —
// it is the same tag vocabulary the firmware's face renderer consumes.
var expressionTagRE = regexp.MustCompile(`\s*(?:\[[a-z]{2,12}\]\s*)+`)

// SanitizeSpokenContent returns only what the child actually heard.
func SanitizeSpokenContent(content string) string {
	kept := make([]string, 0, 4)
	for _, line := range strings.Split(content, "\n") {
		// Tags are stripped per line, after splitting: the pattern eats
		// surrounding whitespace including newlines, which would otherwise
		// destroy the line anchor the MEMO check needs.
		stripped := strings.TrimSpace(expressionTagRE.ReplaceAllString(line, " "))
		// Also drops the bare "MEMO: type=" tail a truncated reply leaves.
		if len(stripped) >= 5 && strings.EqualFold(stripped[:5], "memo:") {
			continue
		}
		if stripped != "" {
			kept = append(kept, stripped)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
