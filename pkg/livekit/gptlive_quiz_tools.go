package livekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/tools"
)

// QuizTrackerConfig wires the tracker to the same bank and reporters the cascade uses.
type QuizTrackerConfig struct {
	Batch     *QuizBatch
	Workspace string
	MemoType  string // "daily_quiz" | "daily_riddle" | "daily_math"

	// AnswerReporter is typed to match NewQuizAnswerReporter exactly, so a
	// caller may wire that reporter in directly. It is invoked on its own
	// background goroutine, after Score has released its internal lock and
	// already returned the directive to the tool caller. The real reporter
	// retries over the network and can take several seconds — that latency
	// must never sit on the tool-call path a child is waiting on, so do not
	// make this call synchronously, and do not rely on separate
	// AnswerReporter calls being ordered relative to each other, to
	// WonderReporter, or to Score's return.
	AnswerReporter func(questionID int64, result string, attempts []QuizAttempt)

	AttemptReporter func(questionID int64, attempts []QuizAttempt)

	// WonderReporter is typed to match NewWonderQuestionReporter exactly, for
	// the same reason and under the same contract as AnswerReporter: invoked
	// on its own background goroutine, not on the caller's, and never
	// assumed to be ordered against anything else.
	WonderReporter func(question, answer, code string)

	Now func() time.Time
}

// QuizTracker owns the game state a GPT-Live session cannot hold in prose: which
// question is pending, how many tries it has had, and what has been scored.
type QuizTracker struct {
	cfg         QuizTrackerConfig
	mu          sync.Mutex
	reported    map[int64]bool
	tries       map[int64]int
	attempts    map[int64][]QuizAttempt
	onDirective func(string)
}

func NewQuizTracker(cfg QuizTrackerConfig) *QuizTracker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MemoType == "" {
		cfg.MemoType = "daily_quiz"
	}
	return &QuizTracker{cfg: cfg, reported: map[int64]bool{}, tries: map[int64]int{}, attempts: map[int64][]QuizAttempt{}}
}

// OnDirective registers the pipeline callback that pushes a Door directive to the voice model.
func (t *QuizTracker) OnDirective(fn func(string)) { t.onDirective = fn }

func (t *QuizTracker) find(id string) *QuizQuestion {
	id = strings.TrimSpace(id)
	for i := range t.cfg.Batch.Questions {
		q := &t.cfg.Batch.Questions[i]
		if q.IDString == id || fmt.Sprint(q.ID) == id {
			return q
		}
	}
	return nil
}

func (t *QuizTracker) pendingLocked() *QuizQuestion {
	for i := range t.cfg.Batch.Questions {
		if !t.reported[t.cfg.Batch.Questions[i].ID] {
			return &t.cfg.Batch.Questions[i]
		}
	}
	return nil
}

// Status is what the backend reads when it needs to know where the game is.
func (t *QuizTracker) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	answered := t.cfg.Batch.AnsweredToday + len(t.reported)
	total := t.cfg.Batch.AnsweredToday + len(t.cfg.Batch.Questions)
	q := t.pendingLocked()
	if q == nil {
		return fmt.Sprintf("STATUS: answered=%d of %d today | all questions done", answered, total)
	}
	tries := t.tries[q.ID]
	return fmt.Sprintf("STATUS: answered=%d of %d today | pending question id=%s (door %d, tries %d): %s",
		answered, total, q.IDString, q.DoorFor(tries), tries, q.Text)
}

// Score records one answer. "miss" is not terminal until the ladder is exhausted.
//
// State mutation happens under t.mu; every callback the caller supplied is
// invoked only AFTER the lock is released — never while holding t.mu, which
// would risk deadlock if a callback re-entered the tracker (Status/Score).
//
// AnswerReporter is then dispatched on its own goroutine, matching the live
// cascade (agent_bridge.go dispatches quizAnswerReporter with `go`, unlocked)
// and the reporter's own documented rationale: it can retry over the network
// for several seconds, and a dropped report is cheaper than seconds of dead
// air after a child's answer is scored. OnDirective, by contrast, is called
// synchronously: it is an in-process handoff of the next Door directive with
// no I/O, so there is no latency to protect against, and calling it inline
// keeps the pipeline's notion of "the active directive" consistent with the
// same string Score just returned.
func (t *QuizTracker) Score(questionID, result, transcript string) (string, error) {
	t.mu.Lock()
	q := t.find(questionID)
	if q == nil {
		t.mu.Unlock()
		return "", fmt.Errorf("question %q is not in today's batch", questionID)
	}
	if t.reported[q.ID] {
		t.mu.Unlock()
		return "", fmt.Errorf("question %s was already scored", q.IDString)
	}
	transcript = strings.TrimSpace(transcript)

	var (
		directive string
		err       error
		answerCB  func()
		nextCB    func()
	)
	switch result {
	case "miss":
		t.tries[q.ID]++
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "wrong", Transcript: transcript})
		if !t.ladderExhausted(q) {
			directive, nextCB = t.nextDirectiveLocked(q)
		} else {
			directive, err, answerCB, nextCB = t.recordLocked(q, "revealed")
		}
	case "correct":
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "correct", Transcript: transcript})
		verdict := "correct"
		if q.DoorFor(t.tries[q.ID]) == doorGuided {
			verdict = "revealed" // ADR-0009: a guided answer does not clear
		}
		directive, err, answerCB, nextCB = t.recordLocked(q, verdict)
	case "revealed":
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "revealed", Transcript: transcript})
		directive, err, answerCB, nextCB = t.recordLocked(q, "revealed")
	default:
		err = errors.New(`result must be "correct", "miss" or "revealed"`)
	}
	t.mu.Unlock()

	if err != nil {
		return "", err
	}
	if answerCB != nil {
		go answerCB()
	}
	if nextCB != nil {
		nextCB() // in-process, no I/O: safe and preferable to call inline
	}
	return directive, nil
}

func (t *QuizTracker) ladderExhausted(q *QuizQuestion) bool {
	tries := t.tries[q.ID]
	if len(q.ChoiceOrder) < 2 && strings.TrimSpace(q.TeachText) == "" {
		return tries >= 2 // no authored ladder: the prompt's own two-miss reveal
	}
	return tries >= doorGuided
}

// recordLocked writes the same MEMO line the cascade parses, then reports.
// Caller holds t.mu. It never invokes a callback itself - it returns one
// (answerCB) for the caller to dispatch once unlocked, per the note on Score.
func (t *QuizTracker) recordLocked(q *QuizQuestion, verdict string) (directive string, err error, answerCB func(), nextCB func()) {
	answered := t.cfg.Batch.AnsweredToday + len(t.reported) + 1
	memo := fmt.Sprintf("MEMO: type=%s | date=%s | scored_q=%s | scored_text=%s | result=%s | answered=%d",
		t.cfg.MemoType, t.cfg.Now().Format("2006-01-02"), q.IDString, strings.ReplaceAll(q.Text, "|", "/"), verdict, answered)
	if _, _, ok := parseQuizVerdict(memo, t.cfg.Batch, t.reported); !ok {
		return "", fmt.Errorf("verdict for question %s did not validate against the bank", q.IDString), nil, nil
	}
	t.reported[q.ID] = true
	if t.cfg.Workspace != "" {
		maybePersistQuizState(t.cfg.Workspace, memo)
	}
	if t.cfg.AnswerReporter != nil {
		id, v, attempts := q.ID, verdict, append([]QuizAttempt(nil), t.attempts[q.ID]...)
		answerCB = func() { t.cfg.AnswerReporter(id, v, attempts) }
	}
	terminal := ""
	if verdict == "revealed" && t.tries[q.ID] > 0 {
		terminal = doorDirectiveText(q, t.tries[q.ID]) // the "all tries used" wording
	}
	next := t.pendingLocked()
	if next == nil {
		return strings.TrimSpace(terminal + "\n\nAll of today's questions are done. Celebrate briefly and move on to free play."), nil, answerCB, nil
	}
	nextDirective, nextCB := t.nextDirectiveLocked(next)
	return strings.TrimSpace(terminal + "\n\n" + nextDirective), nil, answerCB, nextCB
}

// nextDirectiveLocked returns the Door directive for q and, when OnDirective
// was registered, a callback the caller must run once t.mu is released.
func (t *QuizTracker) nextDirectiveLocked(q *QuizQuestion) (directive string, cb func()) {
	d := doorDirectiveText(q, t.tries[q.ID])
	if d == "" {
		d = fmt.Sprintf("## This Question\nAsk question %s plainly, in your own words. Do not offer choices and do not hint yet.", q.IDString)
	}
	if t.onDirective != nil {
		onDirective := t.onDirective
		cb = func() { onDirective(d) }
	}
	return d, cb
}

// RecordWonder reports the child's Wonder answer. RecordWonder touches no
// tracker state, so there is no lock to release first, but WonderReporter is
// still dispatched on its own goroutine per its contract comment on
// QuizTrackerConfig: the real reporter is a network call and must not block
// whatever called RecordWonder (the quiz_record_wonder tool).
func (t *QuizTracker) RecordWonder(question, answer, code string) {
	if t.cfg.WonderReporter != nil {
		go t.cfg.WonderReporter(question, answer, code)
	}
}

// Tools are the three functions the backend model may call.
func (t *QuizTracker) Tools() []tools.Tool {
	return []tools.Tool{quizStatusTool{t}, quizScoreAnswerTool{t}, quizRecordWonderTool{t}}
}

type quizStatusTool struct{ t *QuizTracker }

func (quizStatusTool) Name() string { return "quiz_status" }
func (quizStatusTool) Description() string {
	return "Where today's quiz stands: how many are answered, which question is pending and which Door it is on. Call this before asking a question if unsure."
}
func (quizStatusTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
}
func (q quizStatusTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return tools.NewToolResult(q.t.Status())
}

type quizScoreAnswerTool struct{ t *QuizTracker }

func (quizScoreAnswerTool) Name() string { return "quiz_score_answer" }
func (quizScoreAnswerTool) Description() string {
	return "Score the child's latest answer to a quiz question. Use result=correct when it matches the bank answer, result=miss when it does not (the tool decides hints, choices and when to reveal), result=revealed when the child asked for the answer. Returns the exact instruction for what the voice should do next; follow it."
}
func (quizScoreAnswerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question_id": map[string]any{"type": "string", "description": "The id in parentheses next to the question, e.g. \"11\"."},
			"result":      map[string]any{"type": "string", "enum": []string{"correct", "miss", "revealed"}},
			"transcript":  map[string]any{"type": "string", "description": "What the child said, verbatim."},
		},
		"required": []string{"question_id", "result"},
	}
}
func (q quizScoreAnswerTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	id, _ := args["question_id"].(string)
	result, _ := args["result"].(string)
	transcript, _ := args["transcript"].(string)
	directive, err := q.t.Score(id, result, transcript)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewToolResult(directive)
}

type quizRecordWonderTool struct{ t *QuizTracker }

func (quizRecordWonderTool) Name() string { return "quiz_record_wonder" }
func (quizRecordWonderTool) Description() string {
	return "Record the Wonder Question the child asked today and the answer given, so it can be recalled tomorrow."
}
func (quizRecordWonderTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"answer":   map[string]any{"type": "string"},
			"code":     map[string]any{"type": "string", "description": "The wonder code from the batch, if one was served."},
		},
		"required": []string{"question", "answer"},
	}
}
func (q quizRecordWonderTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	question, _ := args["question"].(string)
	answer, _ := args["answer"].(string)
	code, _ := args["code"].(string)
	q.t.RecordWonder(question, answer, code)
	return tools.SilentResult("recorded")
}
