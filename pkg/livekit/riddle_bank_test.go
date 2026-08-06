package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Riddler reuses the whole quiz machine and differs only by which bank the API
// serves. These tests pin the three seams where that difference travels:
// the placeholder, the fetch query, and the answer payload.

func TestRiddlesPlaceholderRendersLikeQuizQuestions(t *testing.T) {
	batch := &QuizBatch{Level: 1, Band: "6-8", Questions: []QuizQuestion{
		{ID: 1, Text: "I have hands but I cannot clap. What am I?", Answer: "a clock"},
	}}

	quiz := RenderQuizQuestions("Rules.\n{{QUIZ_QUESTIONS}}\nEnd.", batch)
	riddle := RenderQuizQuestions("Rules.\n{{RIDDLES}}\nEnd.", batch)

	if quiz != riddle {
		t.Errorf("placeholders must render identically:\nquiz=%q\nriddle=%q", quiz, riddle)
	}
	if strings.Contains(riddle, "{{RIDDLES}}") {
		t.Error("{{RIDDLES}} was not substituted")
	}
}

func TestRiddlesPlaceholderSubstitutedWhenBatchUnavailable(t *testing.T) {
	// A nil batch must still consume the placeholder. Leaving the raw token in
	// the prompt would put "{{RIDDLES}}" in front of the model, and it has read
	// stray markup aloud before.
	out := RenderQuizQuestions("{{RIDDLES}}", nil)
	if strings.Contains(out, "{{RIDDLES}}") {
		t.Error("nil batch left the placeholder in the prompt")
	}
	if !strings.Contains(out, "unavailable") {
		t.Errorf("expected the no-bank instruction, got %q", out)
	}
}

func TestPromptWithNoPlaceholderIsUntouched(t *testing.T) {
	// Cheeko and Nani must be unaffected by either placeholder existing.
	const prompt = "You are Cheeko. Be warm."
	if got := RenderQuizQuestions(prompt, &QuizBatch{Questions: []QuizQuestion{{ID: 1}}}); got != prompt {
		t.Errorf("prompt without a placeholder was modified: %q", got)
	}
}

func TestFetchQuizBatchSendsCharacterAndReadsBank(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"level":1,"age_band":"6-8","bank":"riddle",
			"questions":[{"id":"1","question_text":"Q?","answer_text":"A"}]}}`))
	}))
	defer srv.Close()

	batch, err := FetchQuizBatch(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
		"key", "AA:BB:CC:DD:EE:FF", "char-123", "Riddler",
	)
	if err != nil {
		t.Fatal(err)
	}

	if batch.Bank != "riddle" {
		t.Errorf("bank not decoded from the response: got %q", batch.Bank)
	}
	if !strings.Contains(gotQuery, "character_id=char-123") {
		t.Errorf("character_id missing from query: %s", gotQuery)
	}
	// The display name is the fallback for sessions whose metadata carries no id.
	if !strings.Contains(gotQuery, "character=Riddler") {
		t.Errorf("character name missing from query: %s", gotQuery)
	}
}

func TestFetchQuizBatchOmitsEmptyCharacterParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":0,"data":{"level":1,"questions":[]}}`))
	}))
	defer srv.Close()

	if _, err := FetchQuizBatch(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
		"key", "AA:BB:CC:DD:EE:FF", "", "",
	); err != nil {
		t.Fatal(err)
	}

	// An empty character_id= would make the API resolve a blank id rather than
	// falling through to the quiz bank.
	if strings.Contains(gotQuery, "character_id=") || strings.Contains(gotQuery, "character=") {
		t.Errorf("empty character params must be omitted entirely: %s", gotQuery)
	}
}

func TestPostQuizAnswerSendsBank(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()

	err := PostQuizAnswer(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
		"key", "AA:BB:CC:DD:EE:FF", 7, "correct", "riddle",
	)
	if err != nil {
		t.Fatal(err)
	}

	if body["bank"] != "riddle" {
		t.Errorf("bank missing from answer payload: %+v", body)
	}
	if body["question_id"] != "7" {
		t.Errorf("question_id changed shape: %+v", body)
	}
}

func TestPostQuizAnswerOmitsEmptyBank(t *testing.T) {
	// A worker that never saw a bank must post exactly what it posted before
	// this change, so the API's quiz default applies.
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()

	if err := PostQuizAnswer(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
		"key", "AA:BB:CC:DD:EE:FF", 7, "correct", "",
	); err != nil {
		t.Fatal(err)
	}

	if _, present := body["bank"]; present {
		t.Errorf("empty bank must be omitted, not sent blank: %+v", body)
	}
}

func TestQuizAnswerReporterCarriesBankFromFetchToPost(t *testing.T) {
	// The round trip that matters: whatever bank the fetch reported is what the
	// verdict is logged against. Getting this wrong writes a riddle verdict into
	// the quiz log, where it clears a quiz level the child never played.
	var fetched *QuizBatch
	var posted map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&posted)
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"level":1,"bank":"riddle",
			"questions":[{"id":"1","question_text":"Q?","answer_text":"A"}]}}`))
	}))
	defer srv.Close()

	cfg := config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}
	fetched, err := FetchQuizBatch(context.Background(), cfg, "key", "AA:BB:CC:DD:EE:FF", "id", "Riddler")
	if err != nil {
		t.Fatal(err)
	}

	report := NewQuizAnswerReporter(cfg, "key", "AA:BB:CC:DD:EE:FF", fetched.Bank)
	report(1, "correct")

	if posted["bank"] != "riddle" {
		t.Errorf("bank did not survive fetch -> report: %+v", posted)
	}
}

func TestPromptWantsQuizBatch(t *testing.T) {
	// The gate main.go uses to decide whether to consume the batch and write the
	// bank state file. A false negative here is the dccd90d failure mode: the
	// batch is discarded and the child is told the bank is unavailable.
	cases := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"quiz placeholder", "Rules\n{{QUIZ_QUESTIONS}}\n", true},
		{"riddle placeholder", "Rules\n{{RIDDLES}}\n", true},
		{"both placeholders", "{{QUIZ_QUESTIONS}}{{RIDDLES}}", true},
		{"neither", "You are Cheeko. Be warm.", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PromptWantsQuizBatch(tc.prompt); got != tc.want {
				t.Errorf("PromptWantsQuizBatch(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

// --- Bank switching -----------------------------------------------------------

// A child who abandons Riddler mid-game and opens Quizzy shares one workspace.
// quiz_bank.md is overwritten at session start so it self-heals, but the MEMO
// scoreboard daily_quiz.md is written only when a MEMO is parsed and would
// survive the switch. Both characters emit type=daily_quiz, so Quizzy would read
// Riddler's "answered=3 | awaiting=<riddle id>" as its own and resume at the
// wrong question.
func TestWriteQuizBankStateClearsScoreboardWhenBankChanges(t *testing.T) {
	ws := t.TempDir()
	memo := filepath.Join(stateDir(ws), "daily_quiz.md")
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)

	riddles := &QuizBatch{Bank: "riddle", Level: 1, Band: "6-8", Questions: []QuizQuestion{
		{ID: 1, Text: "I have hands but cannot clap. What am I?", Answer: "a clock"},
	}}
	quiz := &QuizBatch{Bank: "quiz", Level: 1, Band: "6-8", Questions: []QuizQuestion{
		{ID: 1, Text: "How many legs does a spider have?", Answer: "eight"},
	}}

	if err := WriteQuizBankState(ws, riddles, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memo, []byte("MEMO: type=daily_quiz | date=2026-08-06 | answered=3 | awaiting=4"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Same bank again: mid-game resume must still work.
	if err := WriteQuizBankState(ws, riddles, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(memo); err != nil {
		t.Fatal("same-bank session must keep the scoreboard; resume depends on it")
	}

	// Switching banks: the other game's scoreboard must not be inherited.
	if err := WriteQuizBankState(ws, quiz, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(memo); !os.IsNotExist(err) {
		t.Error("switching banks must clear the previous bank's scoreboard")
	}
}

func TestWriteQuizBankStateRecordsWhichBankPlayed(t *testing.T) {
	ws := t.TempDir()
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	batch := &QuizBatch{Bank: "riddle", Level: 1, Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}}}

	if err := WriteQuizBankState(ws, batch, now); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stateDir(ws), quizBankStateFile))
	if err != nil {
		t.Fatal(err)
	}
	// The header is the only record of which bank last played; the switch
	// detection above reads it back.
	if !strings.Contains(string(data), "bank=riddle") {
		t.Errorf("bank not recorded in the header:\n%s", data)
	}
}

func TestWriteQuizBankStateTreatsAMissingBankAsQuiz(t *testing.T) {
	// Files written before banks existed carry no bank=. Quiz was the only bank
	// then, so a riddle session must still clear their scoreboard.
	ws := t.TempDir()
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(stateDir(ws), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "QUIZ_BANK: type=quiz_bank | date=2026-08-05 | level=1 | band=6-8 | replay=false\n\nold\n"
	if err := os.WriteFile(filepath.Join(stateDir(ws), quizBankStateFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	memo := filepath.Join(stateDir(ws), "daily_quiz.md")
	if err := os.WriteFile(memo, []byte("MEMO: type=daily_quiz | date=2026-08-06"), 0o600); err != nil {
		t.Fatal(err)
	}

	riddles := &QuizBatch{Bank: "riddle", Level: 1, Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}}}
	if err := WriteQuizBankState(ws, riddles, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(memo); !os.IsNotExist(err) {
		t.Error("a legacy bank-less file must be treated as quiz and its scoreboard cleared")
	}
}

func TestWriteQuizBankStateFirstEverSessionIsNotAnError(t *testing.T) {
	ws := t.TempDir()
	batch := &QuizBatch{Bank: "riddle", Level: 1, Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}}}
	if err := WriteQuizBankState(ws, batch, time.Now()); err != nil {
		t.Fatalf("no prior state must not error: %v", err)
	}
}
