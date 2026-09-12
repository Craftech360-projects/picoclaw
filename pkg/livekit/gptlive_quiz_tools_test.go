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

func TestQuizCorrectAtDoorThreeIsRevealed(t *testing.T) {
	rec := newQuizAnswerRecorder()
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: rec.report})
	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	tr.Score("11", "correct", "eight")
	if call := rec.take(t); call.result != "revealed" {
		t.Errorf("mastery rule: Door 3 correct is revealed, got %+v", call)
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
