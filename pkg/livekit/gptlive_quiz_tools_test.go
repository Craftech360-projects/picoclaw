package livekit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func testBatch() *QuizBatch {
	return &QuizBatch{Level: 1, Band: "6-8", Bank: "quiz", AnsweredToday: 2, Questions: []QuizQuestion{
		{ID: 11, IDString: "11", Text: "How many legs does a spider have?", Answer: "eight", ChoiceOrder: []string{"eight", "six"}, TeachText: "four legs each side"},
		{ID: 12, IDString: "12", Text: "What colour is the sky on a clear day?", Answer: "blue"},
	}}
}

// quizAnswerRecorder is a race-safe stand-in for the real AnswerReporter.
// AnswerReporter is invoked on its own background goroutine (see the contract
// comment on QuizTrackerConfig.AnswerReporter), so a test observing its calls
// needs real synchronization, not a plain slice polled with time.Sleep - that
// pattern gives the race detector no happens-before edge between the write and
// the read and fails under -race regardless of timing luck. A buffered channel
// gives every call a synchronization point for free and needs no polling.
type quizAnswerRecorder struct {
	calls chan quizAnswerCall
}

type quizAnswerCall struct {
	questionID int64
	result     string
	attempts   []QuizAttempt
}

func newQuizAnswerRecorder() *quizAnswerRecorder {
	return &quizAnswerRecorder{calls: make(chan quizAnswerCall, 8)}
}

func (r *quizAnswerRecorder) report(questionID int64, result string, attempts []QuizAttempt) {
	r.calls <- quizAnswerCall{questionID: questionID, result: result, attempts: attempts}
}

// take blocks for one recorded call, failing the test if none arrives in time.
func (r *quizAnswerRecorder) take(t *testing.T) quizAnswerCall {
	t.Helper()
	select {
	case c := <-r.calls:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an AnswerReporter call")
		return quizAnswerCall{}
	}
}

// expectNone asserts no call arrives within a short window - used to prove a
// rejected tool call recorded nothing.
func (r *quizAnswerRecorder) expectNone(t *testing.T) {
	t.Helper()
	select {
	case c := <-r.calls:
		t.Fatalf("expected no AnswerReporter call, got %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

func findQuizTool(t *testing.T, tr *QuizTracker, name string) tools.Tool {
	t.Helper()
	for _, tool := range tr.Tools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func TestQuizScoreCorrectPersistsMemoAndReports(t *testing.T) {
	ws := t.TempDir()
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: ws, MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	directive, err := tr.Score("11", "correct", "eight")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(directive, "Ask question 12 plainly") {
		t.Errorf("after a correct answer the directive must move to the next question: %q", directive)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "memory", "state", "daily_quiz.md"))
	if err != nil {
		t.Fatal(err)
	}
	memo := string(raw)
	for _, want := range []string{"MEMO: type=daily_quiz", "scored_q=11", "result=correct", "answered=3"} {
		if !strings.Contains(memo, want) {
			t.Errorf("memo missing %s: %s", want, memo)
		}
	}
	if call := rec.take(t); call.result != "correct" {
		t.Errorf("expected a single reported result=correct, got %+v", call)
	}
	if _, err := tr.Score("11", "correct", "eight"); err == nil {
		t.Error("a question cannot be scored twice")
	}
}

func TestQuizMissesWalkTheLadderAndRevealAtTheEnd(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	d1, _ := tr.Score("11", "miss", "six")
	if !strings.Contains(d1, `"eight" or "six"`) {
		t.Errorf("first miss opens Door 2: %q", d1)
	}
	d2, _ := tr.Score("11", "miss", "ten")
	if !strings.Contains(d2, "four legs each side") {
		t.Errorf("second miss opens Door 3: %q", d2)
	}
	d3, _ := tr.Score("11", "miss", "twelve")
	if !strings.Contains(d3, "all three tries") {
		t.Errorf("third miss is terminal: %q", d3)
	}
	if call := rec.take(t); call.result != "revealed" {
		t.Errorf("exhausted ladder is recorded as revealed, got %+v", call)
	}
	if got := tr.Status(); !strings.Contains(got, "pending question id=12") {
		t.Errorf("status must move on: %s", got)
	}
}

// TestQuizCorrectAtDoorThreeIsRevealed asserts BOTH halves of what a Door-3
// correct answer must do, because for a long time only the first half was
// checked and the second half was broken (final whole-branch review, Critical
// 1). The verdict is downgraded to `revealed` per ADR-0009's mastery rule —
// and recordLocked used to take that downgraded verdict string as its signal
// that the ladder had ended, calling doorDirectiveText(q, 2) for a question
// whose ladder runs to 3. That returns the Door-3 line: "say this explanation
// ... then ask the question again and wait". The question had just been closed
// and pendingLocked had already moved to 12, so the directive handed back to
// the backend model (and pushed into the voice model's live instructions via
// OnDirective) told it to re-ask a finished question immediately before asking
// the next one — and any quiz_score_answer for it came back "already scored".
//
// The old version of this test discarded the returned directive entirely,
// which is exactly why the suite stayed green with that bug in place.
func TestQuizCorrectAtDoorThreeIsRevealed(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	directive, err := tr.Score("11", "correct", "eight")
	if err != nil {
		t.Fatal(err)
	}
	if call := rec.take(t); call.result != "revealed" {
		t.Errorf("mastery rule: Door 3 correct is revealed, got %+v", call)
	}
	if strings.Contains(directive, "ask the question again") || strings.Contains(directive, "four legs each side") {
		t.Errorf("a correct answer closes question 11; the directive must not re-ask or re-explain it: %q", directive)
	}
	if strings.Contains(directive, "question 11") {
		t.Errorf("question 11 is closed and pending has moved on; the directive must not name it at all: %q", directive)
	}
	if !strings.Contains(directive, "Ask question 12 plainly") {
		t.Errorf("the directive must move the session on to the next pending question: %q", directive)
	}
}

// TestQuizCorrectAtUnauthoredDoorTwoDoesNotReaskTheClosedQuestion is the same
// Critical 1 bug one try earlier. A question with no authored Door 2 (fewer
// than two choices) but a TeachText has DoorFor(1) == doorGuided, so a correct
// answer on the SECOND try is downgraded to `revealed` with tries == 1 — while
// ladderExhausted for that shape is still `tries >= doorGuided`. Gating the
// terminal wording on the verdict therefore produced the Door-3 re-ask at
// tries == 1. Gating it on the ladder actually being exhausted does not.
func TestQuizCorrectAtUnauthoredDoorTwoDoesNotReaskTheClosedQuestion(t *testing.T) {
	batch := &QuizBatch{Level: 1, Band: "6-8", Bank: "quiz", Questions: []QuizQuestion{
		{ID: 21, IDString: "21", Text: "What sound does thunder follow?", Answer: "lightning", TeachText: "light travels faster than sound"},
		{ID: 22, IDString: "22", Text: "What colour is grass?", Answer: "green"},
	}}
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: t.TempDir(), MemoType: "daily_quiz"})
	if _, err := tr.Score("21", "miss", "rain"); err != nil {
		t.Fatal(err)
	}
	directive, err := tr.Score("21", "correct", "lightning")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(directive, "question 21") {
		t.Errorf("question 21 was just closed; the directive must not name it: %q", directive)
	}
	if !strings.Contains(directive, "Ask question 22 plainly") {
		t.Errorf("the directive must move on to question 22: %q", directive)
	}
}

// TestQuizExhaustedLadderStillCarriesTheTerminalWording is the guard on the
// other side of Critical 1's fix: narrowing the terminal gate from the verdict
// string to ladderExhausted must not stop the genuinely-exhausted case from
// telling the voice to stop, score the question and move on. Both ladder
// shapes are checked — the authored three-Door one and the unauthored
// two-miss one — because they take different branches of doorDirectiveText.
func TestQuizExhaustedLadderStillCarriesTheTerminalWording(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	directive, err := tr.Score("11", "miss", "twelve")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(directive, "all three tries") {
		t.Errorf("an exhausted authored ladder must still end with the terminal wording: %q", directive)
	}

	// Question 12 has no choices and no teach text: its ladder ends at two misses.
	tr.Score("12", "miss", "purple")
	directive, err = tr.Score("12", "miss", "orange")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(directive, "result=revealed") {
		t.Errorf("an exhausted unauthored ladder must still carry the reveal instruction: %q", directive)
	}
}

// TestQuizTrackerWithoutABatchDoesNotPanic covers Minor 4: find, pendingLocked
// and Status all dereference cfg.Batch, which was safe only because
// buildGPTLiveSpec (one package away) refuses to build a tracker without one.
func TestQuizTrackerWithoutABatchDoesNotPanic(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Workspace: t.TempDir()})
	if got := tr.Status(); !strings.Contains(got, "all questions done") {
		t.Errorf("Status() with no batch = %q", got)
	}
	if _, err := tr.Score("11", "correct", "eight"); err == nil {
		t.Error("Score() with no batch must report the question is not in today's batch, not panic")
	}
	tr.FlushPendingAttempts() // must not panic either
}

// TestQuizFlushPendingAttemptsReportsTheUnfinishedQuestion covers Important 5:
// QuizTrackerConfig.AttemptReporter is wired by main.go but was never invoked
// anywhere on the gptlive path, so a child who tried a question twice and then
// stopped produced no attempt rows at all. persistGPTLiveSession now calls this
// at teardown, the way the cascade calls flushPendingQuizAttempts.
func TestQuizFlushPendingAttemptsReportsTheUnfinishedQuestion(t *testing.T) {
	type call struct {
		id       int64
		attempts []QuizAttempt
	}
	calls := make(chan call, 4)
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AttemptReporter: func(questionID int64, attempts []QuizAttempt) {
			calls <- call{id: questionID, attempts: attempts}
		}})

	// Nothing tried yet: nothing to report.
	tr.FlushPendingAttempts()
	select {
	case c := <-calls:
		t.Fatalf("expected no attempt report before any try, got %+v", c)
	default:
	}

	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	tr.FlushPendingAttempts()

	select {
	case c := <-calls:
		if c.id != 11 {
			t.Errorf("attempt report question id = %d, want 11", c.id)
		}
		if len(c.attempts) != 2 {
			t.Errorf("attempt report carried %d attempts, want the 2 unresolved tries: %+v", len(c.attempts), c.attempts)
		}
	default:
		t.Fatal("AttemptReporter was never invoked for the question the session ended on")
	}

	// Idempotent: a second flush finds nothing.
	tr.FlushPendingAttempts()
	select {
	case c := <-calls:
		t.Fatalf("a second flush must report nothing, got %+v", c)
	default:
	}
}

// TestQuizFlushPendingAttemptsDoesNotDropRowsForQuestionIDZero covers the
// consistency fix the re-review noted: the take and the decision to report must
// be the same decision. An earlier version removed the buffered attempts under
// the lock and THEN discarded the report when the id happened to be 0, which
// silently lost those rows. Not reachable with today's bank, but "we deleted it
// and then dropped it" is not a property worth leaving in place.
func TestQuizFlushPendingAttemptsDoesNotDropRowsForQuestionIDZero(t *testing.T) {
	type call struct {
		id       int64
		attempts []QuizAttempt
	}
	calls := make(chan call, 4)
	batch := &QuizBatch{Level: 1, Band: "6-8", Bank: "quiz", Questions: []QuizQuestion{
		{ID: 0, IDString: "0", Text: "What is two plus two?", Answer: "four"},
	}}
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: t.TempDir(), MemoType: "daily_math",
		AttemptReporter: func(questionID int64, attempts []QuizAttempt) {
			calls <- call{id: questionID, attempts: attempts}
		}})

	if _, err := tr.Score("0", "miss", "five"); err != nil {
		t.Fatal(err)
	}
	tr.FlushPendingAttempts()

	select {
	case c := <-calls:
		if len(c.attempts) != 1 {
			t.Errorf("attempt report carried %d attempts, want 1: %+v", len(c.attempts), c.attempts)
		}
	default:
		t.Fatal("the buffered attempts for question id 0 were taken under the lock and then silently dropped")
	}
}

// TestQuizStateTypesWrittenNamesTheMemoTypeOnceScored covers the other half of
// the character-progress fix (Important 2): CollectStateMemos treats an empty
// written-set as "this session persisted nothing", and on the gptlive path the
// AgentBridge never writes state — the tracker does — so without this the
// progress POST would collect no memos for Quizzy/Bujho/Ginti.
func TestQuizStateTypesWrittenNamesTheMemoTypeOnceScored(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_riddle"})
	if got := tr.StateTypesWritten(); len(got) != 0 {
		t.Errorf("nothing scored yet, want no state types, got %v", got)
	}
	tr.Score("11", "correct", "eight")
	got := tr.StateTypesWritten()
	if !got["daily_riddle"] {
		t.Errorf("StateTypesWritten() = %v, want daily_riddle after a scored question", got)
	}
}

// TestQuizScoreResultIsSelfSufficientForSingleModelVendors covers a live Gemini
// bug: a single-model vendor cannot take mid-session instruction updates, so the
// quiz_score_answer tool result is the ONLY place it learns a question was
// scored. The prompt's static bank block keeps saying "0 scored so far", and a
// result that named only the next question's id let the model re-ask a question
// the child had already answered. The result must therefore say which question
// is closed, how many are done, and give the next question's full text.
func TestQuizScoreResultIsSelfSufficientForSingleModelVendors(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	tool := findQuizTool(t, tr, "quiz_score_answer")

	res := tool.Execute(context.Background(), map[string]any{"question_id": "11", "result": "correct", "transcript": "eight"})
	if res == nil || res.IsError {
		t.Fatalf("score 11 correct: %+v", res)
	}
	got := res.ForLLM
	for _, want := range []string{
		"## This Question",
		"What colour is the sky on a clear day?", // the next question's text
		"id=11",                                  // the closed question is named...
		"How many legs does a spider have?",      // ...with its text
		"already scored",
		"3 of 4", // AnsweredToday 2 + this one, of 2 + 2
	} {
		if !strings.Contains(got, want) {
			t.Errorf("score result missing %q: %q", want, got)
		}
	}

	res = tool.Execute(context.Background(), map[string]any{"question_id": "12", "result": "correct", "transcript": "blue"})
	if res == nil || res.IsError {
		t.Fatalf("score 12 correct: %+v", res)
	}
	got = res.ForLLM
	for _, want := range []string{"All of today's questions are done", "4 of 4", "id=12", "NOT ask"} {
		if !strings.Contains(got, want) {
			t.Errorf("final score result missing %q: %q", want, got)
		}
	}
}

func TestQuizMissResultRepeatsTheSameQuestionText(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	d, err := tr.Score("11", "miss", "six")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"How many legs does a spider have?", "Stay on", "2 of 4"} {
		if !strings.Contains(d, want) {
			t.Errorf("authored-ladder miss result missing %q: %q", want, d)
		}
	}
	if strings.Contains(d, "What colour is the sky") {
		t.Errorf("a miss stays on question 11 and must not name the next question: %q", d)
	}

	// Unauthored ladder (question 12 has no choices, no teach text).
	tr.Score("11", "correct", "eight")
	d, err = tr.Score("12", "miss", "green")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "What colour is the sky on a clear day?") || !strings.Contains(d, "Stay on") {
		t.Errorf("unauthored-ladder miss result must restate the same question: %q", d)
	}
}

func TestQuizToolsExposeTheThreeFunctions(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	var names []string
	for _, tool := range tr.Tools() {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "quiz_status,quiz_score_answer,quiz_record_wonder" {
		t.Errorf("tools: %v", names)
	}
}

// The backend model's tool arguments are untrusted input: an id it invents, or
// a result value it hallucinates outside the tool's declared enum, must be
// rejected explicitly rather than silently scored.

func TestQuizScoreAnswerRejectsUnknownQuestionID(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	tool := findQuizTool(t, tr, "quiz_score_answer")
	result := tool.Execute(context.Background(), map[string]any{
		"question_id": "999", "result": "correct", "transcript": "whatever",
	})
	if result == nil || !result.IsError {
		t.Errorf("expected an error result for a question id not in today's batch, got %+v", result)
	}
	rec.expectNone(t)
}

func TestQuizScoreAnswerRejectsOutOfRangeResultValue(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	tool := findQuizTool(t, tr, "quiz_score_answer")
	result := tool.Execute(context.Background(), map[string]any{
		"question_id": "11", "result": "wrong", "transcript": "eight",
	})
	if result == nil || !result.IsError {
		t.Errorf("expected an error result for a result value outside correct/miss/revealed, got %+v", result)
	}
	rec.expectNone(t)
}
