package livekit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Scored quiz questions come from the curated bank in the Manager DB, not from
// the LLM (ADR-0005). The worker pulls one batch per session and injects it into
// the greeting via {{QUIZ_QUESTIONS}}; the LLM only voices, judges and
// encourages. When the bank is unreachable there is NO scored quiz — invented
// questions are exactly the failure this replaces.

const quizQuestionsPlaceholder = "{{QUIZ_QUESTIONS}}"

// riddlesPlaceholder is an alias, not a second mechanism: Riddler plays the
// identical game against a different bank, and its prompt reads wrong asking for
// "quiz questions". Both substitute the same block.
const riddlesPlaceholder = "{{RIDDLES}}"

// mathProblemsPlaceholder: Ginti, same alias pattern as Riddler — identical
// game, math bank, and a prompt that asks for "problems" not "questions".
const mathProblemsPlaceholder = "{{MATH_PROBLEMS}}"

// QuizQuestion is one curated question as served by GET /quiz/next-questions.
// The API sends ids as strings (Postgres bigint); ID is the parsed form and is
// what the MEMO channel validates verdicts against.
type QuizQuestion struct {
	ID       int64    `json:"-"`
	IDString string   `json:"id"`
	Text     string   `json:"question_text"`
	Answer   string   `json:"answer_text"`
	Accepted []string `json:"accepted_answers"`

	// The Door ladder, assigned by the server (ADR-0005: the model voices, the
	// server decides). AskMode is the STARTING door; the worker escalates
	// through the rungs below as tries are used.
	//
	// ChoiceOrder and TeachText are ABSENT when that Door has not been authored
	// for this question — which is every question until the bank is re-levelled.
	// An unauthored Door is skipped, never improvised: inventing the second
	// option or the explanation is exactly the generated scored content ADR-0005
	// removed.
	AskMode     string   `json:"ask_mode"`
	AttemptNo   int      `json:"attempt_no"`
	ChoiceOrder []string `json:"choice_order"`
	TeachText   string   `json:"teach_text"`
}

// Door numbers. The server names them open|choice|guided; the worker counts
// tries, so it needs the ordinal too.
const (
	doorOpen   = 1
	doorChoice = 2
	doorGuided = 3
)

// DoorFor returns the Door this question is on after `tries` failed attempts,
// skipping any Door the question has no authored content for.
//
// Skipping matters: with an unauthored Door 2, a child who misses twice should
// reach the teaching Door rather than be asked to choose between options that do
// not exist.
func (q QuizQuestion) DoorFor(tries int) int {
	// Clamp FIRST. Skipping unauthored Doors before clamping let a question with
	// no authored content at all land on an empty Door 3 once tries exceeded the
	// ladder — the child would hear an explanation that does not exist.
	door := doorOpen + tries
	if door > doorGuided {
		door = doorGuided
	}
	if door == doorChoice && len(q.ChoiceOrder) < 2 {
		door = doorGuided
	}
	if door == doorGuided && strings.TrimSpace(q.TeachText) == "" {
		// Nothing left to escalate to; hold on the last Door that has content.
		if len(q.ChoiceOrder) >= 2 {
			return doorChoice
		}
		return doorOpen
	}
	return door
}

// QuizBatch is one session's worth of questions: the current Level for this
// device's age band, or a champion-replay level when every level is cleared.
type QuizBatch struct {
	Level     int            `json:"level"`
	Band      string         `json:"age_band"`
	Replay    bool           `json:"replay"`
	Questions []QuizQuestion `json:"questions"`
	// Decided by the server from the answer log, not by the model. Restored
	// transcripts kept telling it "the Daily Ten is complete" on a fresh day.
	AnsweredToday int  `json:"answered_today"`
	DayComplete   bool `json:"day_complete"`
	// Bank is echoed by the API so the verdict can be logged against the same
	// bank the questions came from. The worker never decides this and never
	// needs to know what the value means.
	Bank string `json:"bank"`
	// WonderQuestion is the open, unscored question the LAST session left this
	// child with (M4). Empty for a child who has never been left one.
	WonderQuestion string `json:"wonder_question"`
}

// managerQuizBaseURL resolves the Manager API base the same way the persona pull
// does (env wins over config, then the local default).
func managerQuizBaseURL(cfg config.LiveKitServiceManagerAPIConfig) string {
	for _, value := range []string{os.Getenv("MANAGER_API_URL"), cfg.BaseURL} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return strings.TrimRight(trimmed, "/")
		}
	}
	return "http://localhost:8002/toy"
}

func setQuizServiceKey(req *http.Request, serviceKey string) {
	if strings.TrimSpace(serviceKey) != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}
}

// unwrapQuizEnvelope validates the repo's standard {code,msg,data} envelope and
// returns the raw data payload.
func unwrapQuizEnvelope(body []byte, what string) (json.RawMessage, error) {
	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", what, err)
	}
	if wrapper.Code != 0 {
		return nil, fmt.Errorf("%s api code=%d msg=%s", what, wrapper.Code, wrapper.Msg)
	}
	return wrapper.Data, nil
}

// FetchQuizBatch PULLs the device's current question set. The caller supplies the
// timeout via ctx; a failure must never fail the session (no batch = no scored
// quiz).
func FetchQuizBatch(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	characterID string,
	characterName string,
) (*QuizBatch, error) {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return nil, fmt.Errorf("device mac is empty")
	}

	// The character selects the bank, but the worker cannot name it: agent_code
	// lives on the persona, and this fetch deliberately runs BEFORE the persona
	// pull so the two overlap. Room metadata carries only the id and the display
	// name, so both go out and the API resolves them. Empty values are omitted —
	// a blank character_id= would be resolved as a real (missing) id rather than
	// falling through to the quiz bank.
	endpoint := managerQuizBaseURL(cfg) + "/quiz/next-questions?device_mac=" + url.QueryEscape(deviceMac)
	if id := strings.TrimSpace(characterID); id != "" {
		endpoint += "&character_id=" + url.QueryEscape(id)
	}
	if name := strings.TrimSpace(characterName); name != "" {
		endpoint += "&character=" + url.QueryEscape(name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setQuizServiceKey(req, serviceKey)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("quiz next-questions status=%d body=%s", resp.StatusCode, string(body))
	}

	data, err := unwrapQuizEnvelope(body, "quiz next-questions")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("quiz next-questions returned empty data")
	}

	var batch QuizBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, fmt.Errorf("decode quiz next-questions data: %w", err)
	}

	// String ids that do not parse would break MEMO verdict matching and produce
	// garbage answer POSTs — drop those questions rather than ask them.
	kept := batch.Questions[:0]
	for _, q := range batch.Questions {
		id, err := strconv.ParseInt(strings.TrimSpace(q.IDString), 10, 64)
		if err != nil {
			logger.WarnCF("livekit", "Dropping quiz question with unparseable id", map[string]any{
				"id": q.IDString,
			})
			continue
		}
		q.ID = id
		kept = append(kept, q)
	}
	batch.Questions = kept
	return &batch, nil
}

// QuizAttempt is one try at a question, including the wrong ones that never
// become an answer row. Transcript is what STT heard, empty for silence.
type QuizAttempt struct {
	Verdict    string `json:"verdict"`
	Transcript string `json:"transcript,omitempty"`
}

// PostQuizAnswer logs one verdict against the bank. question_id goes out as a
// string, matching the id form the selection endpoint serves.
func PostQuizAnswer(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	questionID int64,
	result string,
	bank string,
	attempts []QuizAttempt,
) error {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return fmt.Errorf("device mac is empty")
	}

	fields := map[string]any{
		"device_mac":  deviceMac,
		"question_id": strconv.FormatInt(questionID, 10),
		"result":      result,
	}
	// Omitted rather than sent blank: the API rejects an unrecognised bank, and
	// "" is not one of its names. No bank means the quiz default, which is what
	// every caller before this change sent.
	if bank = strings.TrimSpace(bank); bank != "" {
		fields["bank"] = bank
	}
	// Also omitted when empty: the field is optional on the API, and sending []
	// would only add bytes to every turn of a quiz that never needed a retry.
	if len(attempts) > 0 {
		fields["attempts"] = attempts
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}

	return postQuizJSON(ctx, cfg, serviceKey, "/quiz/answer", payload, "quiz answer")
}

// postQuizJSON POSTs a JSON body to a quiz endpoint and validates the envelope.
// Shared so the answer and attempts paths cannot drift in how they authenticate
// or how they decide a response was a failure.
func postQuizJSON(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	path string,
	payload []byte,
	what string,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, managerQuizBaseURL(cfg)+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setQuizServiceKey(req, serviceKey)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s status=%d body=%s", what, resp.StatusCode, string(body))
	}
	if _, err := unwrapQuizEnvelope(body, what); err != nil {
		return err
	}
	return nil
}

// PostQuizAttempts logs tries for a question that never resolved. No verdict is
// sent, because none was reached — the server writes attempt rows only.
func PostQuizAttempts(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	questionID int64,
	bank string,
	attempts []QuizAttempt,
) error {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return fmt.Errorf("device mac is empty")
	}
	if len(attempts) == 0 {
		return nil
	}

	fields := map[string]any{
		"device_mac":  deviceMac,
		"question_id": strconv.FormatInt(questionID, 10),
		"attempts":    attempts,
	}
	if bank = strings.TrimSpace(bank); bank != "" {
		fields["bank"] = bank
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return postQuizJSON(ctx, cfg, serviceKey, "/quiz/attempts", payload, "quiz attempts")
}

// NewQuizAttemptReporter returns the teardown flush handed to the bridge. One
// try, no retry: this runs while the session is closing, and holding teardown
// open to retry a diagnostic write would delay releasing the room.
func NewQuizAttemptReporter(
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	bank string,
) func(questionID int64, attempts []QuizAttempt) {
	return func(questionID int64, attempts []QuizAttempt) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := PostQuizAttempts(ctx, cfg, serviceKey, deviceMac, questionID, bank, attempts); err != nil {
			logger.WarnCF("livekit", "Unresolved attempts not logged", map[string]any{
				"question_id": questionID,
				"attempts":    len(attempts),
				"error":       err.Error(),
			})
			return
		}
		logger.InfoCF("livekit", "Unresolved attempts flushed at teardown", map[string]any{
			"question_id": questionID,
			"attempts":    len(attempts),
		})
	}
}

// PostWonderQuestion saves the open question a session ended on (M4). Unscored:
// it writes no answer row and gates nothing.
func PostWonderQuestion(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	question string,
) error {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return fmt.Errorf("device mac is empty")
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}

	payload, err := json.Marshal(map[string]any{"device_mac": deviceMac, "question": question})
	if err != nil {
		return err
	}
	return postQuizJSON(ctx, cfg, serviceKey, "/quiz/wonder", payload, "wonder question")
}

// NewWonderQuestionReporter returns the teardown save handed to the bridge. One
// try, no retry: a lost wonder question costs one warm opening line next time,
// which is not worth holding a closing session open for.
func NewWonderQuestionReporter(
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
) func(question string) {
	return func(question string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := PostWonderQuestion(ctx, cfg, serviceKey, deviceMac, question); err != nil {
			// The question itself is not logged: it is the child's, and a log
			// line is a second place it would have to be protected.
			logger.WarnCF("livekit", "Wonder question not saved", map[string]any{"error": err.Error()})
			return
		}
		logger.InfoCF("livekit", "Wonder question saved", map[string]any{"chars": len(question)})
	}
}

// NewQuizAnswerReporter returns the per-session verdict reporter handed to the
// bridge. One retry, then log-and-drop: a lost log row costs a repeated question
// tomorrow, which is cheaper than blocking the conversation.
func NewQuizAnswerReporter(
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	bank string,
) func(questionID int64, result string, attempts []QuizAttempt) {
	return func(questionID int64, result string, attempts []QuizAttempt) {
		for attempt := 1; attempt <= 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := PostQuizAnswer(ctx, cfg, serviceKey, deviceMac, questionID, result, bank, attempts)
			cancel()
			if err == nil {
				logger.DebugCF("livekit", "Quiz answer reported", map[string]any{
					"question_id": questionID,
					"result":      result,
					"bank":        bank,
					"attempts":    len(attempts),
				})
				return
			}
			if attempt == 2 {
				logger.WarnCF("livekit", "Quiz answer report failed; dropping", map[string]any{
					"question_id": questionID,
					"result":      result,
					"bank":        bank,
					"attempts":    len(attempts),
					"error":       err.Error(),
				})
				return
			}
			time.Sleep(2 * time.Second)
		}
	}
}

// RenderQuizQuestions substitutes {{QUIZ_QUESTIONS}} in a character's greeting
// prompt. Prompts without the placeholder (Cheeko, Nani, ...) come back
// untouched. A nil or empty batch replaces the placeholder with an explicit
// no-quiz instruction — the LLM may invent unscored content only.
// PromptWantsQuizBatch reports whether a greeting carries either bank
// placeholder, and is therefore a character the batch must be fetched for.
//
// Callers MUST use this rather than testing for a placeholder themselves. main.go
// used to hardcode "{{QUIZ_QUESTIONS}}", so adding {{RIDDLES}} silently sent every
// Riddler session down the no-quiz branch: batch discarded, bank state file never
// written, and the child told the bank was unavailable.
func PromptWantsQuizBatch(prompt string) bool {
	return strings.Contains(prompt, quizQuestionsPlaceholder) ||
		strings.Contains(prompt, riddlesPlaceholder) ||
		strings.Contains(prompt, mathProblemsPlaceholder)
}

func RenderQuizQuestions(prompt string, batch *QuizBatch) string {
	if !PromptWantsQuizBatch(prompt) {
		return prompt
	}
	block := quizQuestionsBlock(batch)
	prompt = strings.ReplaceAll(prompt, quizQuestionsPlaceholder, block)
	prompt = strings.ReplaceAll(prompt, riddlesPlaceholder, block)
	return strings.ReplaceAll(prompt, mathProblemsPlaceholder, block)
}

func quizQuestionsBlock(batch *QuizBatch) string {
	if batch == nil || len(batch.Questions) == 0 {
		return "## Today's Quiz Questions\n" +
			"The question bank is unavailable right now. Do NOT run a scored quiz and " +
			"do NOT invent questions. Offer free chat instead, and tell the child new " +
			"questions are coming soon."
	}

	var b strings.Builder
	// The Wonder Question the child was left with last time (M4). Placed before
	// the questions because it is the opening beat, not part of the scored run.
	if wonder := strings.TrimSpace(batch.WonderQuestion); wonder != "" {
		b.WriteString("## Last Time You Wondered\n")
		b.WriteString(fmt.Sprintf(
			"Before the quiz, warmly remind the child of the question you left them with: %q. ", wonder))
		b.WriteString("Ask if they thought any more about it, listen, and be delighted by whatever they say. ")
		b.WriteString("There is no right answer and it is NOT scored. One short exchange, then start the quiz.\n\n")
	}
	b.WriteString("## Today's Quiz Questions")
	var scope []string
	if batch.Level > 0 {
		scope = append(scope, fmt.Sprintf("Level %d", batch.Level))
	}
	if band := strings.TrimSpace(batch.Band); band != "" {
		scope = append(scope, "band "+band)
	}
	if len(scope) > 0 {
		b.WriteString(" (" + strings.Join(scope, ", ") + ")")
	}
	// State the day's status as fact. This block is re-injected every turn, so
	// it is the strongest evidence in context — stronger than a restored
	// transcript in the model's own voice claiming the day is already done.
	if batch.DayComplete {
		b.WriteString(fmt.Sprintf(
			"\nSTATUS: today's Daily Ten IS complete (%d scored today). Do not ask another scored question today; celebrate and offer one unscored Bonus Buzz.",
			batch.AnsweredToday))
	} else {
		b.WriteString(fmt.Sprintf(
			"\nSTATUS: today's Daily Ten is NOT complete - %d of 10 scored so far today, and the questions below are the ones still to ask. Ignore anything in the conversation or your memory that says today is finished; this line is computed from the record and overrides it.",
			batch.AnsweredToday))
	}
	b.WriteString("\nAsk ONLY these questions, in order, one per turn. Never invent a question.")
	b.WriteString("\nIf the child does not answer, encourage and re-ask the same question — never move on to the next question until this one has been judged.")
	if batch.Replay {
		b.WriteString("\nThese are champion rounds — the child has beaten every level; frame them as a victory lap.")
	}
	for i, q := range batch.Questions {
		// Numbered from answered+1, not 1: after N scored the model announces
		// "question N+1" and hunts for that label. A remainder list restarting
		// at 1 invited it to skip to label N+1 (the 2026-08-06 bees skip).
		b.WriteString(fmt.Sprintf("\n%d. (id=%d) %s — Answer: %s", batch.AnsweredToday+i+1, q.ID, q.Text, q.Answer))
		if alternates := trimmedNonEmpty(q.Accepted); len(alternates) > 0 {
			b.WriteString(" (also accept: " + strings.Join(alternates, ", ") + ")")
		}
	}
	return b.String()
}

func trimmedNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
