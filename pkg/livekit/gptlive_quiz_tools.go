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
	if cfg.Batch == nil {
		// find/pendingLocked/Status all dereference cfg.Batch unguarded. That is
		// safe today only because buildGPTLiveSpec refuses to build a tracker
		// without a batch — an invariant held by one caller, one package away.
		// An empty batch makes it structural instead: find returns nil ("not in
		// today's batch"), pendingLocked returns nil ("all questions done"), and
		// nothing panics.
		cfg.Batch = &QuizBatch{}
	}
	return &QuizTracker{cfg: cfg, reported: map[int64]bool{}, tries: map[int64]int{}, attempts: map[int64][]QuizAttempt{}}
}

// OnDirective registers the pipeline callback that pushes a Door directive to the voice model.
func (t *QuizTracker) OnDirective(fn func(string)) { t.onDirective = fn }

// Workspace is the session workspace this tracker writes its MEMO state into.
// persistGPTLiveSession needs it to collect that state for character progress
// when the AgentBridge has no workspace of its own to offer.
func (t *QuizTracker) Workspace() string {
	if t == nil {
		return ""
	}
	return t.cfg.Workspace
}

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

// dayCompleteLocked reports whether today's scored quiz is over. The batch can
// be longer than what is left of the day, so this, not running out of batch
// questions, is what ends the quiz. The server can also end the day before ten
// (a level finished today: DayComplete with answered_today < 10) and still
// serve a full next-level batch, which must not be scored either. Caller holds
// t.mu.
func (t *QuizTracker) dayCompleteLocked() bool {
	return t.cfg.Batch.DayComplete || t.cfg.Batch.AnsweredToday+len(t.reported) >= dailyQuizTarget
}

func (t *QuizTracker) pendingLocked() *QuizQuestion {
	if t.dayCompleteLocked() {
		return nil
	}
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
	answered, total := t.doneLocked()
	q := t.pendingLocked()
	if q == nil {
		if t.dayCompleteLocked() {
			return fmt.Sprintf("STATUS: answered=%d of %d today | today's Daily Ten is complete, all questions done; do not ask or score another quiz question today", answered, total)
		}
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
	// The hard stop behind the day-complete result: a model that ignores it
	// and asks an eleventh question still cannot write an answer row for it.
	if t.dayCompleteLocked() {
		scored := t.cfg.Batch.AnsweredToday + len(t.reported)
		t.mu.Unlock()
		// No "10 of 10": the server can end the day earlier (a level finished).
		// The Bonus Buzz sentence is there because the persona tells a single
		// model to score every short reply, so its Bonus Buzz lands here too.
		return "", fmt.Errorf("today's Daily Ten is already complete (%d scored today); nothing was scored. Do not ask or score any more quiz questions today. "+
			"An unscored Bonus Buzz answer needs no tool call: just react warmly and carry on", scored)
	}
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
			directive, nextCB = t.nextDirectiveLocked(q, true)
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
	// status=completed on the answer that completes the Daily Ten, where
	// AGENT.md's completed MEMO carries it: Saved State counts a day as
	// finished only when it says so.
	status := ""
	if answered >= dailyQuizTarget {
		status = " | status=completed"
	}
	memo := fmt.Sprintf("MEMO: type=%s | date=%s%s | scored_q=%s | scored_text=%s | result=%s | answered=%d",
		t.cfg.MemoType, t.cfg.Now().Format("2006-01-02"), status, q.IDString, strings.ReplaceAll(q.Text, "|", "/"), verdict, answered)
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
	// The terminal line is gated on the LADDER being exhausted, not on the
	// verdict string. Those are not the same condition: Score downgrades a
	// correct answer given at Door 3 to `revealed` (ADR-0009's mastery rule),
	// and a Door-3 correct answer arrives with tries == 2 — one short of
	// exhaustion. Gating on `verdict == "revealed"` therefore called
	// doorDirectiveText(q, 2), which takes neither the no-ladder branch nor the
	// `tries >= doorGuided` terminal branch and falls through to
	// `switch q.DoorFor(2)` -> doorGuided: "say this explanation ... then ask
	// the question again and wait". The question was just closed (it is in
	// t.reported and pendingLocked has already moved on), so the voice was told
	// to re-ask a finished question immediately before asking the next one, and
	// any quiz_score_answer for it came back "already scored". With an
	// unauthored Door 2 (len(ChoiceOrder) < 2 with TeachText set) DoorFor(1) is
	// already doorGuided, so the same thing happened one try earlier.
	//
	// ladderExhausted is the same predicate the "miss" case already uses to
	// decide a miss is terminal, so the terminal wording appears exactly when
	// the tries really did run out. This mirrors the cascade, which downgrades
	// the verdict only and recomputes doorDirective() from an already-advanced
	// pendingQuizID (agent_bridge.go).
	terminal := ""
	if t.ladderExhausted(q) && t.tries[q.ID] > 0 {
		terminal = doorDirectiveText(q, t.tries[q.ID]) // the "all tries used" wording
	}
	// Single-model vendors (Gemini) cannot take mid-session instruction
	// updates, so this tool result is the only place they learn a question was
	// closed: the prompt's static bank block keeps its stale STATUS forever.
	// Name the closed question by id AND text so it is never re-asked.
	scored := fmt.Sprintf("Previous question id=%s %q is already scored. Do NOT ask it again.", q.IDString, q.Text)
	next := t.pendingLocked()
	if next == nil {
		done, total := t.doneLocked()
		// The shared terminal wording ends "move straight on to the next
		// question", but there is none: keep the reveal/warm-line part and
		// point it at the wrap-up instead. doorDirectiveText is left alone
		// because the cascade shares it.
		terminal = strings.ReplaceAll(terminal, "move straight on to the next question", "then wrap up the quiz")
		var end string
		if t.dayCompleteLocked() {
			end = t.dayCompleteTextLocked()
		} else {
			end = fmt.Sprintf(
				"All of today's questions are done (%d of %d). Celebrate briefly and move on to free play. Do NOT ask any quiz question again today.",
				done, total)
		}
		directive = strings.TrimSpace(terminal + "\n\n" + scored + "\n" + end)
		// Push it too: on GPT-Live the voice model's instructions otherwise still
		// end with the question just answered, the last directive pushed.
		if t.onDirective != nil {
			onDirective := t.onDirective
			nextCB = func() { onDirective(directive) }
		}
		return directive, nil, answerCB, nextCB
	}
	nextDirective, nextCB := t.nextDirectiveLocked(next, false)
	return strings.TrimSpace(terminal + "\n\n" + scored + "\n\n" + nextDirective), nil, answerCB, nextCB
}

// dayCompleteTextLocked is the result for the answer that completes the Daily
// Ten. It closes the day the way the cascade does (AGENT.md 2b and
// wonderClosingDirective): celebrate, then leave the child with ONE Wonder
// Question — the server's when one was served — recorded via
// quiz_record_wonder, since this path has no model-written MEMO to carry it.
// Caller holds t.mu.
func (t *QuizTracker) dayCompleteTextLocked() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today's Daily Ten is complete (%d of %d). Do NOT ask any more quiz questions today. ", dailyQuizTarget, dailyQuizTarget)
	b.WriteString("Celebrate briefly, then leave the child with ONE Wonder Question right now; it is never scored. ")
	b.WriteString("Ask it only this once, not again later or at goodbye. ")
	// The record step names the tool but does not assume the reader can call
	// it: on OpenAI GPT-Live this text is also pushed to the voice model, which
	// has no tools and gets the answer recorded by delegating.
	if ask := t.cfg.Batch.WonderToAsk; ask != nil && strings.TrimSpace(ask.Text) != "" {
		fmt.Fprintf(&b, "Ask EXACTLY this one, in your own warm words but the same question: %q. Not one of your own. ", strings.TrimSpace(ask.Text))
		b.WriteString("When the child answers, get it recorded once with quiz_record_wonder (question = that question, answer = what they said")
		if code := strings.TrimSpace(ask.Code); code != "" {
			fmt.Fprintf(&b, ", code=%s", code)
		}
		b.WriteString("); delegate that if you cannot call tools.")
	} else {
		b.WriteString("Make it a short, open question with no right answer. ")
		b.WriteString("When the child answers, get it recorded once with quiz_record_wonder (question = the question as you asked it, answer = what they said); delegate that if you cannot call tools.")
	}
	return b.String()
}

// doneLocked is today's done count, counted as recordLocked's MEMO `answered`
// is, out of the Daily Ten: both are capped at dailyQuizTarget, so the count
// agrees with the prompt's "N of 10" STATUS line instead of the batch size.
// Caller holds t.mu.
func (t *QuizTracker) doneLocked() (done, total int) {
	done = min(t.cfg.Batch.AnsweredToday+len(t.reported), dailyQuizTarget)
	total = min(t.cfg.Batch.AnsweredToday+len(t.cfg.Batch.Questions), dailyQuizTarget)
	return done, total
}

// nextDirectiveLocked returns the Door directive for q and, when OnDirective
// was registered, a callback the caller must run once t.mu is released.
//
// The Door wording (shared with the cascade) names the question by id only. A
// single-model vendor never receives an instruction update, so the directive
// is made self-sufficient here: the question's full text and today's done
// count. stay marks a miss that keeps the session on the same question.
func (t *QuizTracker) nextDirectiveLocked(q *QuizQuestion, stay bool) (directive string, cb func()) {
	d := doorDirectiveText(q, t.tries[q.ID])
	if d == "" {
		d = fmt.Sprintf("## This Question\nAsk question %s plainly, in your own words. Do not offer choices and do not hint yet.", q.IDString)
	}
	if stay {
		d += fmt.Sprintf("\nStay on this same question; it is NOT scored yet. The question (id=%s) is: %q", q.IDString, q.Text)
	} else {
		d += fmt.Sprintf("\nThe question (id=%s) is: %q", q.IDString, q.Text)
	}
	done, total := t.doneLocked()
	d += fmt.Sprintf("\n%d of %d of today's questions are done. This count replaces any earlier STATUS line.", done, total)
	if t.onDirective != nil {
		onDirective := t.onDirective
		cb = func() { onDirective(d) }
	}
	return d, cb
}

// FlushPendingAttempts reports the tries for the question this session ended
// on without ever resolving — the gptlive equivalent of the cascade's
// flushPendingQuizAttempts (agent_bridge.go), which fires at teardown for
// exactly the same reason. Without it a child who answers a question twice and
// then puts the toy down produces no attempt rows at all, because
// AnswerReporter only ever fires for a question that reached a verdict.
//
// Called from persistGPTLiveSession at teardown. Synchronous on purpose, like
// the cascade's: a goroutine here would race teardown and usually lose.
//
// Safe to call more than once: the buffered attempts are removed under the
// lock, so a second call finds nothing. It deliberately does NOT mark the
// question reported — the question was not scored, only its tries recorded.
func (t *QuizTracker) FlushPendingAttempts() {
	if t == nil || t.cfg.AttemptReporter == nil {
		return
	}
	t.mu.Lock()
	var (
		id       int64
		attempts []QuizAttempt
	)
	// The take and the decision to report are the same decision: an earlier
	// version removed the buffered attempts first and then discarded the report
	// when id happened to be 0, which silently lost those rows. Nothing in
	// today's bank uses id 0, but "we deleted it and then dropped it" is not a
	// property to leave lying around. Take only what will actually be reported.
	if q := t.pendingLocked(); q != nil && len(t.attempts[q.ID]) > 0 {
		id = q.ID
		attempts = append([]QuizAttempt(nil), t.attempts[q.ID]...)
		delete(t.attempts, q.ID)
	}
	t.mu.Unlock()
	if len(attempts) == 0 {
		return
	}
	t.cfg.AttemptReporter(id, attempts)
}

// StateTypesWritten names the MEMO state types this tracker persisted, in the
// shape sendCharacterProgress/CollectStateMemos expect. On the gptlive path
// the AgentBridge never writes state (the tracker's own recordLocked calls
// maybePersistQuizState directly), so bridge.StateTypesWritten() is empty and
// character progress would collect nothing without this.
func (t *QuizTracker) StateTypesWritten() map[string]bool {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.reported) == 0 || strings.TrimSpace(t.cfg.MemoType) == "" {
		return nil
	}
	return map[string]bool{t.cfg.MemoType: true}
}

// RecordWonder reports the child's Wonder answer. It does not deduplicate:
// the manager is the source of truth for that (per code per day, and verbatim
// repeats), and a second call for the same question legitimately fills in an
// answer the first call did not have. RecordWonder touches no tracker state,
// so there is no lock to release first, but WonderReporter is still
// dispatched on its own goroutine per its contract comment on
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
	return "Record TODAY's Wonder Question - the new open question you ask the child after today's Daily Ten (or at goodbye) - and the child's answer, so it can be recalled next time. " +
		"Never use it for last session's Wonder Question when you remind the child of it at the start."
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
