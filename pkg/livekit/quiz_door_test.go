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
		// Needs an authored rung, or there is deliberately no directive to place.
		quizBatch: &QuizBatch{Questions: []QuizQuestion{{
			ID: 7, IDString: "7", Text: "Q", TeachText: "because reasons"}}},
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

// Ticket 011 asked whether Door 3's looser phrasing breaks the anti-invention
// guard. These are hand-written examples and are NOT grounds for relaxing it —
// the ticket is explicit that only real transcripts can justify that. They exist
// to check the opposite claim: that no relaxation is needed.
func TestDoor3PhrasingStillPassesTheGuard(t *testing.T) {
	bank := "How many legs does a spider have?"

	// scored_text is "that same question in a few plain words", so what reaches
	// the guard after a Door 3 turn is still a description of the question, not
	// the teaching sentence.
	door3 := []string{
		"how many legs a spider has",
		"the spider legs question",
		"number of legs on a spider",
		"legs",
	}
	for _, asked := range door3 {
		if !questionTextMatchesBank(asked, bank) {
			t.Errorf("Door 3 paraphrase rejected, which would silently drop a real verdict: %q", asked)
		}
	}

	// And the guard still earns its keep: four invented questions reached the
	// database before it existed.
	invented := []string{
		"what is your favourite colour",
		"can you count to twenty",
	}
	for _, asked := range invented {
		if questionTextMatchesBank(asked, bank) {
			t.Errorf("invented question accepted: %q", asked)
		}
	}
}

// Regression, found by a real session on 2026-08-14. A question with no
// authored Door 2 or Door 3 has no Doors behaviour to drive, so the directive
// must stay silent and leave the character prompt's own ask/hint/reveal flow
// intact. Emitting "ask plainly, do not hint yet" every turn suppressed that
// escape: one child answered the same question wrong eight times across two
// sessions and was never hinted, never told, and never scored.
func TestNoDirectiveWithoutAnAuthoredLadder(t *testing.T) {
	bare := &QuizBatch{Questions: []QuizQuestion{{ID: 226, IDString: "226", Text: "Which part do you smell with?"}}}
	ab := &AgentBridge{quizBatch: bare, pendingQuizID: 226}

	// First ask: the prompt's own flow is correct, so say nothing.
	if got := ab.quizDoorDirective(); got != "" {
		t.Fatalf("first ask needs no directive, got %q", got)
	}

	// One miss: hint. Must not tell the child the answer yet.
	ab.pendingQuizAttempts = make([]QuizAttempt, 1)
	one := ab.quizDoorDirective()
	if !strings.Contains(one, "hint") {
		t.Fatalf("after one miss the child should be hinted: %q", one)
	}
	if strings.Contains(one, "result=revealed") {
		t.Fatalf("one miss must not reveal: %q", one)
	}

	// Two or more: reveal and SCORE. Naming the MEMO fields is the whole point —
	// without a verdict the question is never scored and the child is stuck on
	// it. Observed live: five misses, no verdict, awaiting= frozen on one id.
	for _, tries := range []int{2, 5, 8} {
		ab.pendingQuizAttempts = make([]QuizAttempt, tries)
		got := ab.quizDoorDirective()
		for _, want := range []string{"scored_q=226", "result=revealed", "next question"} {
			if !strings.Contains(got, want) {
				t.Fatalf("after %d misses the directive must contain %q: %q", tries, want, got)
			}
		}
	}

	// One authored rung is enough to take over.
	withChoice := &QuizBatch{Questions: []QuizQuestion{{
		ID: 226, IDString: "226", Text: "Q", ChoiceOrder: []string{"nose", "ear"}}}}
	ab2 := &AgentBridge{quizBatch: withChoice, pendingQuizID: 226}
	if got := ab2.quizDoorDirective(); got == "" {
		t.Fatal("an authored ladder must still drive the turn")
	}
}

// A session that ends mid-question produces no verdict, so its tries were held
// in the bridge and lost on teardown — the child who tried six times and gave up
// left no trace at all. Observed live 2026-08-14 on question 226: eight tries
// across two sessions, zero attempt rows.
func TestPendingAttemptsFlushOnClose(t *testing.T) {
	var gotID int64
	var gotAttempts []QuizAttempt
	ab := &AgentBridge{
		pendingQuizID:       226,
		pendingQuizAttempts: []QuizAttempt{{Verdict: "wrong", Transcript: "the leg"}, {Verdict: "wrong", Transcript: "the ears"}},
		quizAttemptReporter: func(id int64, a []QuizAttempt) { gotID, gotAttempts = id, a },
	}

	ab.flushPendingQuizAttempts()
	if gotID != 226 || len(gotAttempts) != 2 {
		t.Fatalf("expected 2 attempts for question 226, got id=%d n=%d", gotID, len(gotAttempts))
	}
	if gotAttempts[1].Transcript != "the ears" {
		t.Errorf("transcripts must survive the flush: %+v", gotAttempts)
	}

	// Idempotent: teardown paths can overlap, and a double flush must not write
	// the same tries twice.
	gotID, gotAttempts = 0, nil
	ab.flushPendingQuizAttempts()
	if gotID != 0 || gotAttempts != nil {
		t.Fatalf("second flush must be a no-op, got id=%d n=%d", gotID, len(gotAttempts))
	}
}

func TestFlushDoesNothingWithoutPendingTries(t *testing.T) {
	called := false
	ab := &AgentBridge{quizAttemptReporter: func(int64, []QuizAttempt) { called = true }}
	ab.flushPendingQuizAttempts()
	// A question answered first time has no held tries, and a session with no
	// quiz has no pending question. Neither should produce a request.
	ab.pendingQuizID = 5
	ab.flushPendingQuizAttempts()
	if called {
		t.Fatal("flushed with nothing pending")
	}
}

// The Wonder Question (M4): one open, unscored question the child is left with,
// remembered into the next session. Unscored means it must touch nothing else.
func TestWonderQuestionCaptureAndFlush(t *testing.T) {
	var saved string
	ab := &AgentBridge{
		quizBatch:              &QuizBatch{Questions: []QuizQuestion{{ID: 1, IDString: "1"}}},
		reportedQuizIDs:        map[int64]bool{},
		wonderQuestionReporter: func(q string) { saved = q },
	}

	// Captured from the MEMO, on a turn that scores nothing.
	ab.reportQuizVerdict("[happy] Bye for now!\nMEMO: type=daily_quiz | status=completed | wonder=Why does the moon follow you?")
	ab.flushWonderQuestion()
	if saved != "Why does the moon follow you?" {
		t.Fatalf("wonder question not captured: %q", saved)
	}

	// Idempotent: teardown paths overlap, and saving twice would duplicate it.
	saved = ""
	ab.flushWonderQuestion()
	if saved != "" {
		t.Fatalf("second flush must be a no-op, got %q", saved)
	}

	// The latest one wins if the model emits more than one.
	ab.reportQuizVerdict("a\nMEMO: type=daily_quiz | wonder=First")
	ab.reportQuizVerdict("b\nMEMO: type=daily_quiz | wonder=Second")
	ab.flushWonderQuestion()
	if saved != "Second" {
		t.Fatalf("expected the latest wonder question, got %q", saved)
	}
}

func TestNoWonderQuestionSavesNothing(t *testing.T) {
	called := false
	ab := &AgentBridge{
		quizBatch:              &QuizBatch{},
		reportedQuizIDs:        map[int64]bool{},
		wonderQuestionReporter: func(string) { called = true },
	}
	// A session where the model never emitted one, and a blank field.
	ab.reportQuizVerdict("c\nMEMO: type=daily_quiz | status=in_progress | awaiting=5")
	ab.reportQuizVerdict("d\nMEMO: type=daily_quiz | wonder=   ")
	ab.flushWonderQuestion()
	if called {
		t.Fatal("saved a wonder question that was never asked")
	}
}

func TestWonderQuestionAppearsInTheBlock(t *testing.T) {
	batch := &QuizBatch{
		Level: 1, Questions: []QuizQuestion{{ID: 1, IDString: "1", Text: "Q?", Answer: "A"}},
		WonderQuestion: "Where does the wind start?",
	}
	block := quizQuestionsBlock(batch)
	if !strings.Contains(block, "Where does the wind start?") {
		t.Fatalf("the previous wonder question must reach the prompt: %q", block)
	}
	if !strings.Contains(block, "NOT scored") {
		t.Errorf("the block must say it is unscored: %q", block)
	}
	// A child who has never been left one gets no such section.
	bare := quizQuestionsBlock(&QuizBatch{Level: 1, Questions: batch.Questions})
	if strings.Contains(bare, "Last Time You Wondered") {
		t.Errorf("no wonder question means no section: %q", bare)
	}
}
