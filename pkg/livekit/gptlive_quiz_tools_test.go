package livekit

import (
	"context"
	"fmt"
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
	// Question 11 is closed: it may be named only in the "already scored" line
	// (by id=11 and text), never as a question to ask ("question 11").
	if strings.Contains(directive, "question 11") {
		t.Errorf("question 11 is closed and pending has moved on; the directive must not name it as a question to ask: %q", directive)
	}
	for _, want := range []string{`id=11 "How many legs does a spider have?" is already scored`, "3 of 4"} {
		if !strings.Contains(directive, want) {
			t.Errorf("guided-door downgrade result missing %q: %q", want, directive)
		}
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
	// Named only in the "already scored" line, never as a question to ask.
	if strings.Contains(directive, "question 21") {
		t.Errorf("question 21 was just closed; the directive must not name it as a question to ask: %q", directive)
	}
	if !strings.Contains(directive, "id=21") || !strings.Contains(directive, "already scored") || !strings.Contains(directive, "1 of 2") {
		t.Errorf("guided-door downgrade result must name id=21 as already scored with the done count: %q", directive)
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
	// Exhausted but not the last question: the closed one is named as scored and
	// the next question's text and the done count follow.
	for _, want := range []string{`id=11 "How many legs does a spider have?" is already scored`, "3 of 4",
		"What colour is the sky on a clear day?"} {
		if !strings.Contains(directive, want) {
			t.Errorf("exhausted-ladder result missing %q: %q", want, directive)
		}
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
	// Last question of the batch: there is no next question, so the result must
	// not send the model to one directly above "all done, ask nothing more".
	if strings.Contains(directive, "next question") {
		t.Errorf("the last question closed via the ladder must not point at a next question: %q", directive)
	}
	for _, want := range []string{"All of today's questions are done (4 of 4)", `id=12 "What colour is the sky on a clear day?" is already scored`} {
		if !strings.Contains(directive, want) {
			t.Errorf("last-question exhausted-ladder result missing %q: %q", want, directive)
		}
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

// dailyTenBatch is a batch longer than what is left of the day, which is the
// normal shape: the manager serves a full-size batch (plus bonus leftovers)
// whatever answered_today already is.
func dailyTenBatch(answeredToday, size int) *QuizBatch {
	b := &QuizBatch{Level: 3, Band: "6-8", Bank: "quiz", AnsweredToday: answeredToday}
	for i := 0; i < size; i++ {
		id := int64(101 + i)
		b.Questions = append(b.Questions, QuizQuestion{ID: id, IDString: fmt.Sprint(id),
			Text: fmt.Sprintf("Practice question number %d?", id), Answer: "yes"})
	}
	return b
}

// TestQuizDailyTenEndsAtTenNotWhenTheBatchRunsOut covers a live Gemini bug
// (task-8 diagnosis 3): with AnsweredToday=9 and an 11-question batch the tool
// result for the tenth answer said "10 of 20 ... ask question 207", and the
// model scored two more. The day ends at ten, whatever the batch holds.
func TestQuizDailyTenEndsAtTenNotWhenTheBatchRunsOut(t *testing.T) {
	ws := t.TempDir()
	rec := newQuizAnswerRecorder()
	batch := dailyTenBatch(9, 11)
	batch.WonderToAsk = &WonderToAsk{Code: "W7", Text: "If you could build a house out of any food, what would you use?"}
	var pushed []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: ws, MemoType: "daily_quiz", AnswerReporter: rec.report})
	tr.OnDirective(func(d string) { pushed = append(pushed, d) })

	d, err := tr.Score("101", "correct", "yes")
	if err != nil {
		t.Fatal(err)
	}
	if call := rec.take(t); call.questionID != 101 || call.result != "correct" {
		t.Errorf("the tenth answer is still reported: %+v", call)
	}
	for _, want := range []string{
		`id=101 "Practice question number 101?" is already scored`,
		"Today's Daily Ten is complete (10 of 10)",
		"Do NOT ask any more quiz questions today",
		"Celebrate briefly",
		"ONE Wonder Question",
		`"If you could build a house out of any food, what would you use?"`,
		"quiz_record_wonder",
		"code=W7",
		"Ask it only this once, not again later or at goodbye",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("day-complete result missing %q: %q", want, d)
		}
	}
	for _, bad := range []string{"Ask question", "102", "of 20", "of 11"} {
		if strings.Contains(d, bad) {
			t.Errorf("day-complete result must not contain %q: %q", bad, d)
		}
	}
	tr.mu.Lock()
	pending := tr.pendingLocked()
	tr.mu.Unlock()
	if pending != nil {
		t.Errorf("pendingLocked at the Daily Ten = question %s, want nil", pending.IDString)
	}
	if got := tr.Status(); !strings.Contains(got, "answered=10 of 10") || !strings.Contains(got, "all questions done") {
		t.Errorf("Status() at the Daily Ten = %q", got)
	}
	if len(pushed) == 0 || pushed[len(pushed)-1] != d {
		t.Errorf("the day-complete result must also replace the voice model's last question directive, pushed=%q", pushed)
	}

	raw, err := os.ReadFile(filepath.Join(ws, "memory", "state", "daily_quiz.md"))
	if err != nil {
		t.Fatal(err)
	}
	memo := strings.TrimSpace(string(raw))
	for _, want := range []string{"status=completed", "scored_q=101", "result=correct", "answered=10"} {
		if !strings.Contains(memo, want) {
			t.Errorf("tenth MEMO missing %q: %s", want, memo)
		}
	}
	if id, result, ok := parseQuizVerdict(memo, batch, map[int64]bool{}); !ok || id != 101 || result != "correct" {
		t.Errorf("parseQuizVerdict must still accept the completed MEMO: id=%d result=%q ok=%v", id, result, ok)
	}

	// The hard stop: a model that ignores the text still cannot score question 11.
	tool := findQuizTool(t, tr, "quiz_score_answer")
	for _, result := range []string{"correct", "miss", "revealed"} {
		res := tool.Execute(context.Background(), map[string]any{"question_id": "102", "result": result, "transcript": "yes"})
		if res == nil || !res.IsError {
			t.Fatalf("scoring past the Daily Ten (result=%s) must be refused, got %+v", result, res)
		}
		if !strings.Contains(res.ForLLM, "Daily Ten is already complete") || !strings.Contains(res.ForLLM, "not ask") {
			t.Errorf("refusal must tell the model the day is done and not to ask more: %q", res.ForLLM)
		}
	}
	rec.expectNone(t)
}

func TestQuizDailyTenFromZeroStopsAtTenInAThirteenQuestionBatch(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: dailyTenBatch(0, 13), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	for i := 1; i <= 9; i++ {
		d, err := tr.Score(fmt.Sprint(100+i), "correct", "yes")
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("%d of 10 of today's questions are done", i); !strings.Contains(d, want) {
			t.Errorf("after answer %d the count must read %q: %q", i, want, d)
		}
	}
	d, err := tr.Score("110", "correct", "yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "Today's Daily Ten is complete (10 of 10)") || strings.Contains(d, "Ask question 111") {
		t.Errorf("the tenth answer ends the day: %q", d)
	}
	// No served Wonder Question: the model asks one of its own and records it.
	for _, want := range []string{"ONE Wonder Question", "quiz_record_wonder"} {
		if !strings.Contains(d, want) {
			t.Errorf("day-complete result without a served wonder missing %q: %q", want, d)
		}
	}
	if _, err := tr.Score("111", "correct", "yes"); err == nil {
		t.Error("question 11 of the day must be refused")
	}
}

func TestQuizMissOnTheTenthQuestionStillStaysOnIt(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: dailyTenBatch(9, 11), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	d, err := tr.Score("101", "miss", "no")
	if err != nil {
		t.Fatalf("a miss before the cap must still be accepted: %v", err)
	}
	for _, want := range []string{"Stay on", "Practice question number 101?", "9 of 10"} {
		if !strings.Contains(d, want) {
			t.Errorf("miss result missing %q: %q", want, d)
		}
	}
	if strings.Contains(d, "Daily Ten is complete") {
		t.Errorf("a miss does not complete the day: %q", d)
	}
	rec.expectNone(t)

	// A later correct answer on the same question completes the day, once.
	d, err = tr.Score("101", "correct", "yes")
	if err != nil {
		t.Fatalf("a correct answer after a miss on question ten must be accepted: %v", err)
	}
	if !strings.Contains(d, "Today's Daily Ten is complete (10 of 10)") {
		t.Errorf("the correct answer after the miss completes the day: %q", d)
	}
	if call := rec.take(t); call.questionID != 101 || call.result != "correct" || len(call.attempts) != 2 {
		t.Errorf("want one report for 101 correct carrying both tries, got %+v", call)
	}
	rec.expectNone(t)
}

func TestQuizExhaustedLadderOnTheTenthQuestionWrapsUp(t *testing.T) {
	batch := dailyTenBatch(9, 11)
	batch.Questions[0].ChoiceOrder = []string{"yes", "no"}
	batch.Questions[0].TeachText = "it is always yes"
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: t.TempDir(), MemoType: "daily_quiz"})
	for i := 1; i <= 2; i++ {
		if _, err := tr.Score("101", "miss", "no"); err != nil {
			t.Fatalf("miss %d on question ten must be accepted: %v", i, err)
		}
	}
	d, err := tr.Score("101", "miss", "no")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"all three tries", "then wrap up the quiz", "Today's Daily Ten is complete (10 of 10)"} {
		if !strings.Contains(d, want) {
			t.Errorf("exhausted ladder at the Daily Ten missing %q: %q", want, d)
		}
	}
	if strings.Contains(d, "next question") || strings.Contains(d, "Ask question") {
		t.Errorf("the last question of the day must not point at a next question: %q", d)
	}
}

// The day-complete result is also pushed through AppendInstructions, which the
// gptlive session truncates past 1600 characters (maxAppendChars). The longest
// shape (exhausted ladder + served wonder, long texts) must fit whole, or the
// recording step at the end is what gets cut.
func TestQuizDayCompletePushFitsTheAppendCap(t *testing.T) {
	batch := dailyTenBatch(9, 11)
	batch.Questions[0].Text = strings.Repeat("Which of these animals lives in the ocean and breathes air? ", 2)
	batch.Questions[0].ChoiceOrder = []string{"whale", "shark"}
	batch.Questions[0].TeachText = "whales come up to breathe"
	batch.WonderToAsk = &WonderToAsk{Code: "W123", Text: strings.Repeat("If you could talk to any animal for a whole day, which would it be? ", 2)}
	var pushed string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: t.TempDir(), MemoType: "daily_quiz"})
	tr.OnDirective(func(d string) { pushed = d })
	for i := 0; i < 3; i++ {
		if _, err := tr.Score("101", "miss", "shark"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len([]rune(gptLiveDirectiveSupersedes + pushed)); n > 1600 {
		t.Errorf("pushed day-complete block is %d chars, over the 1600-char append cap", n)
	}
	if !strings.HasSuffix(pushed, "delegate that if you cannot call tools.") {
		t.Errorf("pushed day-complete block must end with the recording step: %q", pushed)
	}
}

// TestQuizServerDayCompleteIsNotScored covers review I1: the manager also ends
// the day when a level is finished (day_complete with answered_today < 10) and
// still serves a full next-level batch. None of it may be scored today.
func TestQuizServerDayCompleteIsNotScored(t *testing.T) {
	rec := newQuizAnswerRecorder()
	batch := dailyTenBatch(6, 10)
	batch.DayComplete = true
	tr := NewQuizTracker(QuizTrackerConfig{Batch: batch, Workspace: t.TempDir(), MemoType: "daily_quiz", AnswerReporter: rec.report})
	if got := tr.Status(); strings.Contains(got, "pending question") || !strings.Contains(got, "Daily Ten is complete") {
		t.Errorf("Status() on a server-completed day = %q", got)
	}
	_, err := tr.Score("101", "correct", "yes")
	if err == nil {
		t.Fatal("a question on a server-completed day must be refused")
	}
	for _, want := range []string{"already complete (6 scored today)", "nothing was scored", "Bonus Buzz answer needs no tool call"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %q", want, err.Error())
		}
	}
	if strings.Contains(err.Error(), "10 of 10") {
		t.Errorf("the day ended by level at six; the refusal must not claim 10 of 10: %q", err.Error())
	}
	rec.expectNone(t)
}

func TestQuizSessionStartingAfterTheDailyTenIsNotScored(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: dailyTenBatch(10, 10), Workspace: t.TempDir(), MemoType: "daily_quiz", AnswerReporter: rec.report})
	if got := tr.Status(); !strings.Contains(got, "answered=10 of 10") || strings.Contains(got, "pending question") {
		t.Errorf("Status() at session start with ten already scored = %q", got)
	}
	res := findQuizTool(t, tr, "quiz_score_answer").Execute(context.Background(),
		map[string]any{"question_id": "101", "result": "correct", "transcript": "yes"})
	if res == nil || !res.IsError || !strings.Contains(res.ForLLM, "Bonus Buzz") {
		t.Errorf("scoring after the Daily Ten must be refused with the Bonus Buzz note, got %+v", res)
	}
	rec.expectNone(t)
	if got := tr.StateTypesWritten(); len(got) != 0 {
		t.Errorf("a refused score must write no state, got %v", got)
	}
}

// TestQuizRecordWonderReportsOncePerSession covers review I2: the Wonder
// Question can be asked after question ten and again at goodbye; the second
// record must not send a second report.
func TestQuizRecordWonderReportsOncePerSession(t *testing.T) {
	reports := make(chan string, 4)
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		WonderReporter: func(question, _, code string) { reports <- code }})
	tool := findQuizTool(t, tr, "quiz_record_wonder")
	args := map[string]any{"question": "q", "answer": "a", "code": "W7"}
	if res := tool.Execute(context.Background(), args); res == nil || res.IsError || res.ForLLM != "recorded" {
		t.Fatalf("first record: %+v", res)
	}
	res := tool.Execute(context.Background(), args)
	if res == nil || res.IsError || !strings.Contains(res.ForLLM, "already recorded") {
		t.Fatalf("a second record is a no-op success that says so, got %+v", res)
	}
	select {
	case <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("the first record never reached WonderReporter")
	}
	select {
	case c := <-reports:
		t.Fatalf("a second record sent a second report (code %s)", c)
	case <-time.After(50 * time.Millisecond):
	}
}

// On OpenAI GPT-Live the voice model has no tools, so the Wonder answer is
// recorded only if its delegation block tells it to delegate.
func TestQuizDelegationBlockCoversTheWonderQuestion(t *testing.T) {
	if got := gptLiveDelegationBlock("English", true); !strings.Contains(got, "quiz_record_wonder") {
		t.Errorf("delegation block must send the Wonder answer to the helper: %q", got)
	}
}

func TestQuizBatchRunningOutBelowTenPushesTheEnding(t *testing.T) {
	var pushed []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	tr.OnDirective(func(d string) { pushed = append(pushed, d) })
	if _, err := tr.Score("11", "correct", "eight"); err != nil {
		t.Fatal(err)
	}
	d, err := tr.Score("12", "correct", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "All of today's questions are done (4 of 4)") {
		t.Errorf("batch-exhausted result = %q", d)
	}
	if len(pushed) != 2 || pushed[1] != d {
		t.Errorf("the batch-exhausted ending must be pushed as the current directive, pushed=%q", pushed)
	}
}
