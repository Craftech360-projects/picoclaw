package session

import "testing"

// What reaches the store is what the child heard. Tags drive the toy's face and
// the MEMO carries session state; neither is ever spoken, and both were being
// saved into the conversation the parent app renders.
func TestSanitizeSpokenContent(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"strips a leading tag", "[excited] Tak! Two is right!", "Tak! Two is right!"},
		{"strips several tags mid-line", "[happy] Good! [curious] Ready?", "Good! Ready?"},
		{"drops the MEMO line", "[proud] Well done!\nMEMO: type=spell_bee | current_level=2", "Well done!"},
		{"drops a truncated MEMO tail", "[happy] Bye!\nMEMO: type=", "Bye!"},
		{"drops a tagged MEMO line", "[warm] Night!\n[neutral] MEMO: type=story | beat=3_of_6", "Night!"},
		{"keeps ordinary brackets-free speech", "Seven ate nine!", "Seven ate nine!"},
		{"keeps multi-line speech", "[happy] One.\nTwo.", "One.\nTwo."},
		{"empties a turn that was only metadata", "[excited]\nMEMO: type=daily_jokes", ""},
		{"leaves plain text untouched", "", ""},
	}
	for _, c := range cases {
		if got := SanitizeSpokenContent(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Square brackets that are not expression tags must survive: the pattern is
// deliberately narrow (2-12 lowercase letters), and over-stripping would edit
// the child's actual conversation.
func TestSanitizeSpokenContentLeavesNonTagBrackets(t *testing.T) {
	for _, in := range []string{
		"The answer is [42]",
		"Say [A] or [B]",
		"[VERYLONGWORDINDEEDHERE] stays",
	} {
		if got := SanitizeSpokenContent(in); got != in {
			t.Errorf("SanitizeSpokenContent(%q) = %q, want unchanged", in, got)
		}
	}
}
