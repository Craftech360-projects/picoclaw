package livekit

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestDoorForSkipsUnauthoredDoors(t *testing.T) {
	full := QuizQuestion{ChoiceOrder: []string{"eight", "six"}, TeachText: "four each side"}
	if got := full.DoorFor(0); got != doorOpen {
		t.Errorf("no tries should be Door 1, got %d", got)
	}
	if got := full.DoorFor(1); got != doorChoice {
		t.Errorf("one miss should be Door 2, got %d", got)
	}
	if got := full.DoorFor(2); got != doorGuided {
		t.Errorf("two misses should be Door 3, got %d", got)
	}
	if got := full.DoorFor(9); got != doorGuided {
		t.Errorf("Door 3 is the last rung, got %d", got)
	}

	// The state of the entire bank until it is re-levelled: no authored content
	// for either escalation. A child must not be asked to choose between options
	// that do not exist, nor hear an empty explanation.
	bare := QuizQuestion{}
	for _, tries := range []int{0, 1, 2, 5} {
		if got := bare.DoorFor(tries); got != doorOpen {
			t.Errorf("unauthored question should stay on Door 1 after %d tries, got %d", tries, got)
		}
	}

	// Door 2 authored but not Door 3: escalate once, then hold.
	choiceOnly := QuizQuestion{ChoiceOrder: []string{"eight", "six"}}
	if got := choiceOnly.DoorFor(2); got != doorChoice {
		t.Errorf("with no teach_text the ladder holds at Door 2, got %d", got)
	}

	// Door 3 authored but not Door 2: skip the missing rung rather than stall.
	teachOnly := QuizQuestion{TeachText: "four each side"}
	if got := teachOnly.DoorFor(1); got != doorGuided {
		t.Errorf("missing Door 2 should skip to Door 3, got %d", got)
	}
}

func TestQuizDoorDirective(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{{
		ID: 42, IDString: "42", Text: "How many legs?", Answer: "eight",
		ChoiceOrder: []string{"eight", "six"}, TeachText: "four on each side",
	}}}

	t.Run("no batch means no directive", func(t *testing.T) {
		ab := &AgentBridge{}
		if got := ab.quizDoorDirective(); got != "" {
			t.Fatalf("Cheeko and Nani must get nothing, got %q", got)
		}
	})

	t.Run("no pending question means no directive", func(t *testing.T) {
		ab := &AgentBridge{quizBatch: batch}
		if got := ab.quizDoorDirective(); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a pending question not in the batch is ignored", func(t *testing.T) {
		ab := &AgentBridge{quizBatch: batch, pendingQuizID: 999}
		if got := ab.quizDoorDirective(); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("Door 1 asks plainly", func(t *testing.T) {
		ab := &AgentBridge{quizBatch: batch, pendingQuizID: 42}
		got := ab.quizDoorDirective()
		if !strings.Contains(got, "plainly") || strings.Contains(got, "six") {
			t.Fatalf("Door 1 must not leak the options: %q", got)
		}
	})

	t.Run("Door 2 carries both options in the server's order", func(t *testing.T) {
		ab := &AgentBridge{quizBatch: batch, pendingQuizID: 42,
			pendingQuizAttempts: []QuizAttempt{{Verdict: "wrong"}}}
		got := ab.quizDoorDirective()
		if !strings.Contains(got, "eight") || !strings.Contains(got, "six") {
			t.Fatalf("Door 2 must carry both options: %q", got)
		}
	})

	t.Run("Door 3 teaches and forbids saying the answer", func(t *testing.T) {
		ab := &AgentBridge{quizBatch: batch, pendingQuizID: 42,
			pendingQuizAttempts: []QuizAttempt{{Verdict: "wrong"}, {Verdict: "wrong"}}}
		got := ab.quizDoorDirective()
		if !strings.Contains(got, "four on each side") {
			t.Fatalf("Door 3 must carry teach_text: %q", got)
		}
		// The handback is mandatory: supplying the answer here would make it
		// `revealed`, the behaviour this redesign removes.
		if !strings.Contains(got, "Do NOT say the answer") {
			t.Fatalf("Door 3 must forbid giving the answer: %q", got)
		}
	})
}

func TestBuildMessagesPutsTheDoorLast(t *testing.T) {
	// Placement is load-bearing: the cache breakpoint is on the static system
	// block and OpenAI caching is prefix-based, so a per-turn directive inserted
	// at the system anchor would invalidate the prefix every single turn.
	ab := &AgentBridge{
		quizBatch:     &QuizBatch{Questions: []QuizQuestion{{ID: 7, IDString: "7", Text: "Q"}}},
		pendingQuizID: 7,
	}
	history := []providers.Message{{Role: "user", Content: "hello"}}
	msgs := ab.buildMessages(history, "", "", "session")

	last := msgs[len(msgs)-1]
	if last.Role != "system" || !strings.Contains(last.Content, "This Question") {
		t.Fatalf("the Door directive must be the LAST message, got role=%q content=%q", last.Role, last.Content)
	}
	for i, m := range msgs[:len(msgs)-1] {
		if strings.Contains(m.Content, "This Question") {
			t.Fatalf("Door directive leaked into message %d, ahead of the conversation", i)
		}
	}
}
