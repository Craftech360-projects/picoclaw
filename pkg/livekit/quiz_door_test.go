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

// The model cannot obey "leave them a NEW question" without knowing the old
// ones. On dev it re-read its own session summaries and asked the food-house
// question again with two words changed, nine days apart.
func TestAlreadyWonderedListReachesTheBlock(t *testing.T) {
	batch := &QuizBatch{
		Level: 1, Questions: []QuizQuestion{{ID: 1, IDString: "1", Text: "Q?", Answer: "A"}},
		RecentWonderQuestions: []string{"If you could fly like a bee, where would you go?", "  ", "Why do stars come out at night?"},
	}
	block := quizQuestionsBlock(batch)
	for _, want := range []string{"Already Wondered", "fly like a bee", "stars come out", "must be about something else"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q in: %s", want, block)
		}
	}
	// A blank entry rendered as an empty pair of quotes reads as a real question.
	if strings.Contains(block, `""`) {
		t.Errorf("blank history entry reached the prompt: %q", block)
	}
	// Nothing asked yet means no section at all.
	bare := quizQuestionsBlock(&QuizBatch{Level: 1, Questions: batch.Questions})
	if strings.Contains(bare, "Already Wondered") {
		t.Errorf("no history means no section: %q", bare)
	}
}

// Storing a question the child has already been asked is how the loop restarts:
// it becomes the newest row, and the next session recalls it as if it were new.
func TestWonderQuestionEchoOfAnyRecentOneIsDropped(t *testing.T) {
	newBridge := func() *AgentBridge {
		return &AgentBridge{
			quizBatch: &QuizBatch{
				WonderQuestion:        "Where does the wind start?",
				RecentWonderQuestions: []string{"Where does the wind start?", "If you could fly like a bee, where would you go?"},
			},
			reportedQuizIDs: map[int64]bool{},
		}
	}

	// The one this session opened by recalling, re-punctuated.
	ab := newBridge()
	ab.reportQuizVerdict("a\nMEMO: type=daily_quiz | wonder=where does the wind start")
	if ab.pendingWonderQuestion != "" {
		t.Errorf("echo of the recalled question was captured: %q", ab.pendingWonderQuestion)
	}

	// An older one from the history list, which used to slip through.
	ab = newBridge()
	ab.reportQuizVerdict("b\nMEMO: type=daily_quiz | wonder=If you could fly like a bee, where would you go?")
	if ab.pendingWonderQuestion != "" {
		t.Errorf("echo of an older question was captured: %q", ab.pendingWonderQuestion)
	}

	// Genuinely new curiosity still gets through — this guard must not eat it.
	ab = newBridge()
	ab.reportQuizVerdict("c\nMEMO: type=daily_quiz | wonder=What do fish dream about?")
	if ab.pendingWonderQuestion != "What do fish dream about?" {
		t.Errorf("a new question must be kept, got %q", ab.pendingWonderQuestion)
	}
}

// The ladder must END. DoorFor clamps at Door 3, so without a terminal state
// every try past the third re-issued "ask again, do not say the answer" forever.
// Observed live 2026-08-14: six wrong tries on one authored question, no verdict,
// no answer row. Same bug as the unauthored branch, fixed there first, missed here.
func TestAuthoredLadderTerminatesAfterThreeTries(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{{
		ID: 184, IDString: "184", Text: "What colour is a banana?", Answer: "yellow",
		ChoiceOrder: []string{"yellow", "something else"}, TeachText: "think of one you have eaten",
	}}}
	ab := &AgentBridge{quizBatch: batch, pendingQuizID: 184}

	// Tries 3+ must all be the terminal directive: score it and move on.
	for _, tries := range []int{3, 4, 6, 9} {
		ab.pendingQuizAttempts = make([]QuizAttempt, tries)
		got := ab.quizDoorDirective()
		for _, want := range []string{"scored_q=184", "result=revealed", "next question"} {
			if !strings.Contains(got, want) {
				t.Fatalf("after %d tries the ladder must terminate with %q: %q", tries, want, got)
			}
		}
		// The answer is deliberately NOT supplied — the question returns another
		// day. Telling it here would be the old reveal behaviour back again.
		if !strings.Contains(got, "do NOT tell the child the answer") {
			t.Fatalf("the terminal turn must not give the answer: %q", got)
		}
	}

	// And the rungs below are untouched: try 2 is still Door 3 teaching.
	ab.pendingQuizAttempts = make([]QuizAttempt, 2)
	if got := ab.quizDoorDirective(); !strings.Contains(got, "think of one you have eaten") {
		t.Fatalf("try 3 (2 misses) should still teach: %q", got)
	}
}

// REAL corpus, ticket 011. Every pair below is a scored_text the live model
// actually emitted in a session on 2026-08-14, against the bank question it was
// judging. This is the evidence the ticket asked for, and it says something the
// design did not expect: the model reproduces the question VERBATIM rather than
// paraphrasing it. The guard's one-word threshold has enormous headroom.
func TestGuardAgainstRealScoredText(t *testing.T) {
	corpus := []struct{ bank, said string }{
		{"What is five plus seven?", "What is five plus seven?"},
		{"What do bees make that we can eat?", "What do bees make that we can eat?"},
		{"Which part of your body do you use to smell?", "Which part of your body do you use to smell?"},
		{"Which planet do we live on?", "Which planet do we live on?"},
		{"What colour do you get when you mix red and yellow?", "What colour do you get when you mix red and yellow?"},
		{"How many legs does a spider have?", "How many legs does a spider have?"},
	}
	rejected := 0
	for _, c := range corpus {
		if !questionTextMatchesBank(c.said, c.bank) {
			rejected++
			t.Errorf("REAL verdict rejected by the guard: bank=%q said=%q", c.bank, c.said)
		}
	}
	// False-reject rate on real data: 0 of 6. No relaxation is warranted.
	if rejected != 0 {
		t.Fatalf("false-reject rate %d/%d — the guard is dropping real verdicts", rejected, len(corpus))
	}
}

// A restored MEMO is a quotation, not a new question.
//
// A session starts by restoring the previous one's completed MEMO from
// MEMORY.md and restating it, `wonder=` included. Taken at face value that read
// as a second moment of curiosity, and teardown saved it again. On dev
// 2026-08-15 the same question landed twice, minutes apart, from two sessions
// where the child had wondered once. Same disease as the day gate reading
// "complete" out of a restored transcript.
//
// The strings below are the real ones from that session.
func TestRestatedWonderQuestionIsNotSavedAgain(t *testing.T) {
	const asked = "If you could have any superpower, what would it be?"

	saved := ""
	ab := &AgentBridge{
		// The server told us what the child was already left with — this is the
		// question rendered as the opening beat.
		quizBatch:              &QuizBatch{WonderQuestion: asked},
		reportedQuizIDs:        map[int64]bool{},
		wonderQuestionReporter: func(q string) { saved = q },
	}

	ab.reportQuizVerdict("[happy] Good afternoon, Kishore!\nMEMO: type=daily_quiz | date=2026-08-15 | status=completed | answered=10 | wonder=" + asked)
	ab.flushWonderQuestion()
	if saved != "" {
		t.Fatalf("restated the recalled question and saved it again: %q", saved)
	}

	// Restating rarely comes back byte for byte.
	ab.reportQuizVerdict(`x
MEMO: type=daily_quiz | wonder="If you could have ANY superpower, what would it be"`)
	ab.flushWonderQuestion()
	if saved != "" {
		t.Fatalf("re-punctuated restatement got through: %q", saved)
	}

	// The session's own new question still saves — this is the whole mechanic,
	// and a guard that ate it would be worse than the duplicate it prevents.
	ab.reportQuizVerdict("y\nMEMO: type=daily_quiz | wonder=I wonder if fish ever dream about swimming in a giant bubble of juice?")
	ab.flushWonderQuestion()
	if saved != "I wonder if fish ever dream about swimming in a giant bubble of juice?" {
		t.Fatalf("a genuinely new wonder question was dropped: %q", saved)
	}
}

// With nothing recalled, every question is new — a first-ever session must not
// be silently swallowed by an empty comparison.
func TestFirstEverWonderQuestionIsSaved(t *testing.T) {
	saved := ""
	ab := &AgentBridge{
		quizBatch:              &QuizBatch{},
		reportedQuizIDs:        map[int64]bool{},
		wonderQuestionReporter: func(q string) { saved = q },
	}
	ab.reportQuizVerdict("z\nMEMO: type=daily_quiz | wonder=Where does the sky end?")
	ab.flushWonderQuestion()
	if saved != "Where does the sky end?" {
		t.Fatalf("first wonder question not saved: %q", saved)
	}
}
