package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBatch() *QuizBatch {
	return &QuizBatch{Level: 1, Band: "6-8", Bank: "quiz", AnsweredToday: 2, Questions: []QuizQuestion{
		{ID: 11, IDString: "11", Text: "How many legs does a spider have?", Answer: "eight", ChoiceOrder: []string{"eight", "six"}, TeachText: "four legs each side"},
		{ID: 12, IDString: "12", Text: "What colour is the sky on a clear day?", Answer: "blue"},
	}}
}

func TestQuizScoreCorrectPersistsMemoAndReports(t *testing.T) {
	ws := t.TempDir()
	var reported []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: ws, MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { reported = append(reported, result) }})
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
	waitFor(t, func() bool { return len(reported) == 1 && reported[0] == "correct" })
	if _, err := tr.Score("11", "correct", "eight"); err == nil {
		t.Error("a question cannot be scored twice")
	}
}

func TestQuizMissesWalkTheLadderAndRevealAtTheEnd(t *testing.T) {
	var results []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { results = append(results, result) }})
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
	waitFor(t, func() bool { return len(results) == 1 })
	if results[0] != "revealed" {
		t.Errorf("exhausted ladder is recorded as revealed, got %s", results[0])
	}
	if got := tr.Status(); !strings.Contains(got, "pending question id=12") {
		t.Errorf("status must move on: %s", got)
	}
}

func TestQuizCorrectAtDoorThreeIsRevealed(t *testing.T) {
	var results []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { results = append(results, result) }})
	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	tr.Score("11", "correct", "eight")
	waitFor(t, func() bool { return len(results) == 1 })
	if results[0] != "revealed" {
		t.Errorf("mastery rule: Door 3 correct is revealed, got %s", results[0])
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

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
