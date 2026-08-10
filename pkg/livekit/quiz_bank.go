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

// QuizQuestion is one curated question as served by GET /quiz/next-questions.
// The API sends ids as strings (Postgres bigint); ID is the parsed form and is
// what the MEMO channel validates verdicts against.
type QuizQuestion struct {
	ID       int64    `json:"-"`
	IDString string   `json:"id"`
	Text     string   `json:"question_text"`
	Answer   string   `json:"answer_text"`
	Accepted []string `json:"accepted_answers"`
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
) error {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return fmt.Errorf("device mac is empty")
	}

	fields := map[string]string{
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

	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}

	endpoint := managerQuizBaseURL(cfg) + "/quiz/answer"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		return fmt.Errorf("quiz answer status=%d body=%s", resp.StatusCode, string(body))
	}
	if _, err := unwrapQuizEnvelope(body, "quiz answer"); err != nil {
		return err
	}
	return nil
}

// NewQuizAnswerReporter returns the per-session verdict reporter handed to the
// bridge. One retry, then log-and-drop: a lost log row costs a repeated question
// tomorrow, which is cheaper than blocking the conversation.
func NewQuizAnswerReporter(
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	bank string,
) func(questionID int64, result string) {
	return func(questionID int64, result string) {
		for attempt := 1; attempt <= 2; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := PostQuizAnswer(ctx, cfg, serviceKey, deviceMac, questionID, result, bank)
			cancel()
			if err == nil {
				logger.DebugCF("livekit", "Quiz answer reported", map[string]any{
					"question_id": questionID,
					"result":      result,
					"bank":        bank,
				})
				return
			}
			if attempt == 2 {
				logger.WarnCF("livekit", "Quiz answer report failed; dropping", map[string]any{
					"question_id": questionID,
					"result":      result,
					"bank":        bank,
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
		strings.Contains(prompt, riddlesPlaceholder)
}

func RenderQuizQuestions(prompt string, batch *QuizBatch) string {
	if !PromptWantsQuizBatch(prompt) {
		return prompt
	}
	block := quizQuestionsBlock(batch)
	prompt = strings.ReplaceAll(prompt, quizQuestionsPlaceholder, block)
	return strings.ReplaceAll(prompt, riddlesPlaceholder, block)
}

func quizQuestionsBlock(batch *QuizBatch) string {
	if batch == nil || len(batch.Questions) == 0 {
		return "## Today's Quiz Questions\n" +
			"The question bank is unavailable right now. Do NOT run a scored quiz and " +
			"do NOT invent questions. Offer free chat instead, and tell the child new " +
			"questions are coming soon."
	}

	var b strings.Builder
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
