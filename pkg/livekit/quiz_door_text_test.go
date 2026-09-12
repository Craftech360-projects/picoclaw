package livekit

import (
	"strings"
	"testing"
)

func TestDoorDirectiveTextLadder(t *testing.T) {
	q := &QuizQuestion{ID: 7, IDString: "7", ChoiceOrder: []string{"eight", "six"}, TeachText: "four legs each side"}
	if got := doorDirectiveText(q, 0); !strings.Contains(got, "Ask question 7 plainly") {
		t.Errorf("door 1: %q", got)
	}
	if got := doorDirectiveText(q, 1); !strings.Contains(got, `"eight" or "six"`) {
		t.Errorf("door 2: %q", got)
	}
	if got := doorDirectiveText(q, 2); !strings.Contains(got, "four legs each side") || !strings.Contains(got, "Do NOT say the answer") {
		t.Errorf("door 3: %q", got)
	}
	if got := doorDirectiveText(q, 3); !strings.Contains(got, "all three tries") {
		t.Errorf("terminal: %q", got)
	}
	bare := &QuizQuestion{ID: 8, IDString: "8"}
	if got := doorDirectiveText(bare, 0); got != "" {
		t.Errorf("unauthored first ask must be empty, got %q", got)
	}
	if got := doorDirectiveText(bare, 1); !strings.Contains(got, "missed question 8 once") {
		t.Errorf("unauthored second: %q", got)
	}
	if got := doorDirectiveText(bare, 2); !strings.Contains(got, "result=revealed") {
		t.Errorf("unauthored reveal: %q", got)
	}
}
