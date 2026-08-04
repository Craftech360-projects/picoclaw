package livekit

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Runtime state lives in per-type files under memory/state/ so it (a) is
// injected into the LLM context by pkg/agent (MemoryStore.ReadStateFiles)
// without tool calls, (b) round-trips through manager workspace sync (the sync
// walker uploads everything non-excluded), and (c) survives voice-context
// summarization, which only trims in-session chat history. One file per MEMO
// type= (quiz scoreboard, story progress) means characters never clobber each
// other's state — the failure that hit the shared single-section design.
const (
	stateDirName    = "state"
	stateLedgerSfx  = "-ledger.md"
	quizStateMaxAge = 48 * time.Hour // ponytail: tz-blind bound; per-device timezone if parents complain

	storyLedgerFile    = "story-ledger.md"
	questionLedgerFile = "question-ledger.md"
	storyLedgerMaxAge  = 30 * 24 * time.Hour // prompt's "do not repeat within thirty days"
	quizLedgerMaxAge   = 14 * 24 * time.Hour // prompt's "within fourteen days"
)

// Legacy single-section markers (pre per-type files). Kept for migration only.
const (
	quizStateHeading     = "## Quiz State"
	quizStateStartMarker = "<!-- quiz-state:start -->"
	quizStateEndMarker   = "<!-- quiz-state:end -->"
)

var quizMemoDateRE = regexp.MustCompile(`(?i)\bdate\s*=\s*(\d{4}-\d{2}-\d{2})`)
var memoTypeRE = regexp.MustCompile(`(?i)\btype\s*=\s*([a-z0-9_-]+)`)

func stateDir(workspace string) string {
	return filepath.Join(workspace, "memory", stateDirName)
}

// quizBankStateFile holds the curated questions selected for this session.
//
// It lives in memory/state/ rather than in a chat message because
// MemoryStore.ReadStateFiles re-injects every file there into the system prompt
// on EVERY turn, while a chat message is evicted the moment history compacts.
// Observed live 2026-08-04: history collapsed 24 -> 8 messages mid-quiz, the
// injected list went with it, and Quizzy invented the rest of the Daily Ten
// while still numbering it correctly from the surviving scoreboard.
const quizBankStateFile = "quiz_bank.md"

// WriteQuizBankState persists the session's curated questions so they survive
// history compaction and cold restarts. A nil or empty batch REMOVES the file:
// serving yesterday's list would let a child be asked questions the server
// never selected for them.
func WriteQuizBankState(workspace string, batch *QuizBatch, now time.Time) error {
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	path := filepath.Join(stateDir(workspace), quizBankStateFile)
	if batch == nil || len(batch.Questions) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(stateDir(workspace), 0o755); err != nil {
		return err
	}

	var sb strings.Builder
	// The date= is what PruneStaleStateFiles matches on; without it the file is
	// kept forever (prune is fail-open on unparseable dates).
	fmt.Fprintf(&sb, "QUIZ_BANK: type=quiz_bank | date=%s | level=%d | band=%s | replay=%t\n\n",
		now.Format("2006-01-02"), batch.Level, batch.Band, batch.Replay)
	sb.WriteString(RenderQuizQuestions("{{QUIZ_QUESTIONS}}", batch))
	sb.WriteString("\n")
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

// questionTextMatchesBank reports whether the question the model says it just
// judged is recognisably the bank question bearing that id.
//
// Quizzy is instructed to ask warmly in her own words, so this tolerates
// padding and punctuation; it exists to catch a wholly invented question being
// reported against a real id, which is how four false "cleared" rows reached
// the database on 2026-08-04. An empty asked-text is accepted: nothing to
// check, and dropping real answers is worse than missing a guard.
func questionTextMatchesBank(asked, bank string) bool {
	askedWords := contentWords(asked)
	bankWords := contentWords(bank)
	if len(askedWords) == 0 || len(bankWords) == 0 {
		return true
	}
	overlap := 0
	for word := range askedWords {
		if bankWords[word] {
			overlap++
		}
	}
	// Asymmetric on purpose: the model is told to name the question "in a few
	// plain words", so it abbreviates ("days in a week" for "How many days are
	// there in one week?"). Requiring a share of the BANK's words rejected that
	// valid answer live. What matters is that what it DID write belongs to the
	// bank question; an invented question shares nothing but filler, which is
	// stripped above.
	return overlap*5 >= len(askedWords)*3
}

var quizFillerWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "do": true,
	"does": true, "you": true, "we": true, "what": true, "which": true,
	"when": true, "how": true, "can": true, "tell": true, "me": true,
	"to": true, "of": true, "in": true, "on": true, "and": true, "it": true,
	"that": true, "this": true, "your": true, "my": true, "ooh": true,
}

func contentWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.Fields(strings.ToLower(s)) {
		word := strings.Trim(field, ".,!?;:'\"()-")
		if word == "" || quizFillerWords[word] {
			continue
		}
		out[word] = true
	}
	return out
}

// extractQuizMemoLine returns the last line-anchored "MEMO:" line of an
// assistant reply, with expression tags stripped. "" when absent.
func extractQuizMemoLine(content string) string {
	last := ""
	// Tags are stripped per line, after splitting: the global tag regex eats
	// surrounding whitespace including the newline before "[neutral] MEMO:",
	// which would destroy the line anchor.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(voiceExpressionTagRE.ReplaceAllString(line, " "))
		if len(trimmed) < 5 || !strings.EqualFold(trimmed[:5], "memo:") {
			continue
		}
		body := strings.TrimSpace(trimmed[5:])
		if body == "" {
			continue
		}
		last = "MEMO: " + body
	}
	return last
}

// stateTypeFromMemo returns the memo's type= value, or "" when missing/empty.
// A missing type usually means the model truncated its own MEMO mid-line
// (observed live: a reply ending in exactly "MEMO: type="); such fragments
// must never overwrite good state.
func stateTypeFromMemo(memo string) string {
	m := memoTypeRE.FindStringSubmatch(memo)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// memoField extracts a pipe-delimited "name=value" field from a MEMO line.
func memoField(memo, name string) string {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\s*=\s*([^|]*)`)
	m := re.FindStringSubmatch(memo)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// quizVerdictResults is the closed set the answer log accepts; anything else the
// model improvises ("skipped", "partly") is dropped rather than POSTed.
var quizVerdictResults = map[string]bool{"correct": true, "wrong": true, "revealed": true}

// parseQuizVerdict returns (questionID, result, ok). ok=false when the MEMO is
// not a daily_quiz one, has no q=/result= fields, the result is not
// correct|wrong|revealed, the id was already reported, or the id is not in the
// batch — unless exactly one batch question is still unreported, in which case
// the id is corrected to that question (the 31B model echoes ids imperfectly,
// and when only one question can possibly be meant there is nothing to confuse).
// A nil batch means the fetch failed, so there is no scored quiz to report on.
func parseQuizVerdict(memo string, batch *QuizBatch, reported map[int64]bool) (int64, string, bool) {
	if batch == nil || stateTypeFromMemo(memo) != "daily_quiz" {
		return 0, "", false
	}
	result := strings.ToLower(memoField(memo, "result"))
	if !quizVerdictResults[result] {
		return 0, "", false
	}
	// scored_q, never awaiting: every turn both judges the previous answer and
	// asks the next question, so reading the pending id logged each answer one
	// question late (observed live 2026-08-04).
	raw := memoField(memo, "scored_q")
	if raw == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		logger.WarnCF("livekit", "Quiz MEMO has unparseable scored_q=; verdict dropped", map[string]any{"scored_q": raw})
		return 0, "", false
	}
	scoredText := memoField(memo, "scored_text")

	known := false
	bankText := ""
	pending := make([]int64, 0, len(batch.Questions))
	for _, q := range batch.Questions {
		if q.ID == id {
			known = true
			bankText = q.Text
		}
		if !reported[q.ID] {
			pending = append(pending, q.ID)
		}
	}
	if known {
		if reported[id] {
			logger.WarnCF("livekit", "Quiz verdict already reported; dropped as duplicate", map[string]any{
				"question_id": id,
				"result":      result,
			})
			return 0, "", false
		}
		// The id alone cannot show that the question actually ASKED was the one
		// bearing it: four invented questions were logged against real bank ids
		// before this check existed.
		if !questionTextMatchesBank(scoredText, bankText) {
			logger.WarnCF("livekit", "Quiz verdict does not match the bank question; dropped as invented", map[string]any{
				"question_id":   id,
				"bank_question":  bankText,
				"asked_question": scoredText,
			})
			return 0, "", false
		}
		return id, result, true
	}
	if len(pending) == 1 {
		logger.InfoCF("livekit", "Quiz MEMO id not in batch; corrected to the only pending question", map[string]any{
			"memo_id":     id,
			"question_id": pending[0],
			"result":      result,
		})
		return pending[0], result, true
	}
	logger.WarnCF("livekit", "Quiz MEMO id not in batch; verdict dropped", map[string]any{
		"question_id": id,
		"pending":     len(pending),
	})
	return 0, "", false
}

// maybePersistQuizState is the per-turn hook: extract the MEMO from a finished
// assistant reply and save it to its per-type state file. Never fails the turn.
func maybePersistQuizState(workspace, assistantContent string) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return
	}
	memo := extractQuizMemoLine(assistantContent)
	if memo == "" {
		return
	}
	stateType := stateTypeFromMemo(memo)
	if stateType == "" {
		logger.WarnCF("livekit", "MEMO has missing/empty type; not persisted (likely truncated)", map[string]any{
			"memo_len": len(memo),
		})
		return
	}
	dir := stateDir(workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF("livekit", "Failed to create state dir", map[string]any{"dir": dir, "error": err.Error()})
		return
	}
	path := filepath.Join(dir, stateType+".md")
	if err := os.WriteFile(path, []byte(memo+"\n"), 0o600); err != nil {
		logger.WarnCF("livekit", "Failed to persist MEMO state", map[string]any{"path": path, "error": err.Error()})
		return
	}
	logger.InfoCF("livekit", "Persisted quiz MEMO to MEMORY.md", map[string]any{
		"path":     path,
		"type":     stateType,
		"memo_len": len(memo),
	})
	updateLedgers(dir, stateType, memo)
}

// updateLedgers maintains the long-horizon no-repeat ledgers that outlive the
// 48h state files. Best-effort: ledger failures never affect the turn.
func updateLedgers(dir, stateType, memo string) {
	date := memoField(memo, "date")
	if date == "" {
		return
	}
	switch stateType {
	case "story":
		if !strings.EqualFold(memoField(memo, "completed"), "true") {
			return
		}
		key := memoField(memo, "story_key")
		if key == "" {
			return
		}
		line := fmt.Sprintf("%s | %s | %s | %s", date, key, memoField(memo, "title"), memoField(memo, "theme"))
		appendLedgerLine(filepath.Join(dir, storyLedgerFile), line, key, storyLedgerMaxAge)
	case "daily_quiz":
		keys := memoField(memo, "asked_keys")
		if keys == "" {
			return
		}
		line := fmt.Sprintf("%s | asked_keys=%s", date, keys)
		upsertLedgerLineByDate(filepath.Join(dir, questionLedgerFile), date, line, quizLedgerMaxAge)
	}
}

// appendLedgerLine appends unless dedupeKey already appears, then prunes by age.
func appendLedgerLine(path, line, dedupeKey string, maxAge time.Duration) {
	existing, _ := os.ReadFile(path)
	if dedupeKey != "" && strings.Contains(string(existing), dedupeKey) {
		return
	}
	lines := append(nonEmptyLines(string(existing)), line)
	writeLedger(path, pruneLedgerLines(lines, time.Now(), maxAge))
}

// upsertLedgerLineByDate replaces the line for the same date (cumulative MEMOs
// re-persist every turn), then prunes by age.
func upsertLedgerLineByDate(path, date, line string, maxAge time.Duration) {
	existing, _ := os.ReadFile(path)
	out := make([]string, 0, 8)
	for _, l := range nonEmptyLines(string(existing)) {
		if !strings.HasPrefix(l, date) {
			out = append(out, l)
		}
	}
	out = append(out, line)
	writeLedger(path, pruneLedgerLines(out, time.Now(), maxAge))
}

func nonEmptyLines(s string) []string {
	out := make([]string, 0, 8)
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func pruneLedgerLines(lines []string, now time.Time, maxAge time.Duration) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if m := quizMemoDateRE.FindStringSubmatch("date=" + l); m != nil {
			if d, err := time.Parse("2006-01-02", m[1]); err == nil && now.Sub(d) > maxAge {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

func writeLedger(path string, lines []string) {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		logger.WarnCF("livekit", "Failed to write ledger", map[string]any{"path": path, "error": err.Error()})
	}
}

// MigrateLegacyQuizStateSection moves a pre-file-era "## Quiz State" marker
// section out of MEMORY.md into memory/state/<type>.md. Idempotent; call at
// session bootstrap. Exported for cmd/picoclaw-livekit.
func MigrateLegacyQuizStateSection(workspace string) {
	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	data, err := os.ReadFile(memoryPath)
	if err != nil {
		return
	}
	s := string(data)
	start := strings.Index(s, quizStateStartMarker)
	if start < 0 {
		return
	}
	memo := ""
	if end := strings.Index(s[start:], quizStateEndMarker); end >= 0 {
		memo = extractQuizMemoLine(s[start : start+end])
	}
	if t := stateTypeFromMemo(memo); t != "" {
		path := filepath.Join(stateDir(workspace), t+".md")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.MkdirAll(stateDir(workspace), 0o755)
			_ = os.WriteFile(path, []byte(memo+"\n"), 0o600)
		}
	}
	if err := os.WriteFile(memoryPath, []byte(removeQuizStateSection(s)), 0o600); err == nil {
		logger.InfoCF("livekit", "Migrated legacy quiz-state section to state file", map[string]any{
			"workspace": workspace,
		})
	}
}

// removeQuizStateSection strips the legacy heading, markers, and everything
// between them — tolerating a missing start OR end marker so a
// truncation-decapitated section still gets cleaned up.
func removeQuizStateSection(s string) string {
	if start := strings.Index(s, quizStateStartMarker); start >= 0 {
		if end := strings.Index(s[start:], quizStateEndMarker); end >= 0 {
			s = s[:start] + s[start+end+len(quizStateEndMarker):]
		} else {
			s = s[:start]
		}
	} else if end := strings.Index(s, quizStateEndMarker); end >= 0 {
		head := s[:end]
		if memoAt := strings.LastIndex(head, "MEMO:"); memoAt >= 0 {
			if nl := strings.LastIndex(head[:memoAt], "\n"); nl >= 0 {
				head = head[:nl+1]
			} else {
				head = ""
			}
		}
		s = head + s[end+len(quizStateEndMarker):]
	}
	s = strings.ReplaceAll(s, quizStateHeading+"\n", "")
	s = strings.ReplaceAll(s, quizStateHeading, "")
	return s
}

// PruneStaleStateFiles removes per-type state files whose date= is older than
// 48h (a returning child starts fresh instead of resuming last week's quiz or
// story) and age-prunes ledger files. Called once per session bootstrap.
// Exported for cmd/picoclaw-livekit. Unparseable dates are kept (fail-open).
func PruneStaleStateFiles(workspace string, now time.Time) (removed int, err error) {
	dir := stateDir(workspace)
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return 0, nil
		}
		return 0, rerr
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if strings.HasSuffix(strings.ToLower(e.Name()), stateLedgerSfx) {
			maxAge := storyLedgerMaxAge
			if strings.EqualFold(e.Name(), questionLedgerFile) {
				maxAge = quizLedgerMaxAge
			}
			if data, err := os.ReadFile(path); err == nil {
				writeLedger(path, pruneLedgerLines(nonEmptyLines(string(data)), now, maxAge))
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		m := quizMemoDateRE.FindStringSubmatch(string(data))
		if m == nil {
			continue
		}
		d, perr := time.Parse("2006-01-02", m[1])
		if perr != nil || now.Sub(d) <= quizStateMaxAge {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed, nil
}

// --- Daily category rotation -------------------------------------------------
//
// A small LLM asked for "a trivia question for a six year old" lands on the same
// canonical handful (banana, moo, giraffe) every day, so the quiz felt repetitive
// across days. Rotating which category opens the day is a positive instruction,
// which steers a 31B model far better than "do not repeat yourself".
//
// This is prompt-driven, not character-gated: a character opts in by putting
// {{TODAY_PLAN}} / {{TODAY_DATE}} / {{TIME_BAND}} in its greeting_prompt.
// Prompts without the placeholders are returned untouched.

// quizCategories mirrors the "Daily question plan" section of the Quizzy prompt.
var quizCategories = []string{
	"animals and nature",
	"numbers and logic",
	"words, sounds and rhymes",
	"colours, shapes and patterns",
	"everyday science and the human body",
	"India and the wider world",
	"safe everyday knowledge",
}

// quizPlanForDay returns the category order for one device on one day: the same
// list rotated by (day + device) so consecutive days open differently and two
// children on the same day do not get an identical plan.
// ponytail: rotation only, not a shuffle — 7 orders is plenty and it stays
// trivially predictable when debugging. Swap for a seeded shuffle if the
// within-day order ever matters.
func quizPlanForDay(seedKey string, now time.Time) []string {
	n := len(quizCategories)
	offset := (now.YearDay() + int(crc32.ChecksumIEEE([]byte(seedKey)))) % n
	out := make([]string, 0, n)
	out = append(out, quizCategories[offset:]...)
	out = append(out, quizCategories[:offset]...)
	return out
}

// promptTimeBand maps a clock to the product's four bands. Uses Asia/Kolkata —
// the same default the prompt's "## Current Time" section uses — so the two
// never disagree in context.
func promptTimeBand(now time.Time) string {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		loc = time.FixedZone("IST", 5*3600+1800)
	}
	t := now.In(loc)
	hm := t.Hour()*60 + t.Minute()
	switch {
	case hm >= 5*60 && hm < 12*60:
		return "morning"
	case hm >= 12*60 && hm < 17*60:
		return "afternoon"
	case hm >= 17*60 && hm < 20*60+30:
		return "evening"
	default:
		return "night"
	}
}

// renderPromptPlaceholders substitutes the per-day placeholders in a character's
// greeting prompt. Cheap no-op for prompts that do not use them.
func renderPromptPlaceholders(prompt, seedKey string, now time.Time) string {
	if !strings.Contains(prompt, "{{TODAY_PLAN}}") && !strings.Contains(prompt, "{{TODAY_DATE}}") &&
		!strings.Contains(prompt, "{{TIME_BAND}}") {
		return prompt
	}
	prompt = strings.ReplaceAll(prompt, "{{TODAY_PLAN}}", strings.Join(quizPlanForDay(seedKey, now), ", "))
	prompt = strings.ReplaceAll(prompt, "{{TODAY_DATE}}", now.Format("Monday 2006-01-02"))
	prompt = strings.ReplaceAll(prompt, "{{TIME_BAND}}", promptTimeBand(now))
	return prompt
}
