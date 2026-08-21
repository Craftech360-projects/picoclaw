package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractQuizMemoLine(t *testing.T) {
	cases := map[string]string{
		// plain, memo on its own line
		"[happy] Ting! Correct!\nMEMO: type=daily_quiz | date=2026-08-01 | answered=1": "MEMO: type=daily_quiz | date=2026-08-01 | answered=1",
		// leading expression tag on the memo line itself
		"Bye!\n[neutral] MEMO: type=daily_quiz | answered=2": "MEMO: type=daily_quiz | answered=2",
		// two MEMO lines -> last wins
		"MEMO: old state\nGreat job!\nMEMO: type=daily_quiz | answered=3": "MEMO: type=daily_quiz | answered=3",
		// no memo at all
		"[happy] Question one! Which animal says moo?": "",
		// memo mentioned mid-sentence, not line-anchored -> not extracted
		"I write a MEMO: note sometimes": "",
		// empty memo body -> ignored
		"Bye!\nMEMO:": "",
	}
	for in, want := range cases {
		if got := extractQuizMemoLine(in); got != want {
			t.Errorf("extractQuizMemoLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateTypeFromMemo(t *testing.T) {
	cases := map[string]string{
		"MEMO: type=daily_quiz | date=2026-08-01": "daily_quiz",
		"MEMO: type=story | beat=2_of_6":          "story",
		"MEMO: type=STORY | x=1":                  "story",
		// the live truncation: reply ended exactly here — must NOT parse
		"MEMO: type=":           "",
		"MEMO: date=2026-08-01": "",
		"MEMO: answered=3":      "",
	}
	for in, want := range cases {
		if got := stateTypeFromMemo(in); got != want {
			t.Errorf("stateTypeFromMemo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseQuizVerdict(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{{ID: 1}, {ID: 2}}}
	cases := []struct {
		name, memo string
		reported   map[int64]bool
		wantID     int64
		wantResult string
		wantOK     bool
	}{
		{"valid correct", "MEMO: type=daily_quiz | date=2026-08-04 | scored_q=1 | result=correct | answered=1", map[int64]bool{}, 1, "correct", true},
		{"valid revealed", "MEMO: type=daily_quiz | scored_q=2 | result=revealed", map[int64]bool{}, 2, "revealed", true},
		{"valid wrong", "MEMO: type=daily_quiz | scored_q=2 | result=wrong | answered=2", map[int64]bool{}, 2, "wrong", true},
		{"result casing tolerated", "MEMO: type=daily_quiz | scored_q=1 | result=Correct", map[int64]bool{}, 1, "correct", true},
		{"no q field", "MEMO: type=daily_quiz | date=2026-08-04 | answered=1", map[int64]bool{}, 0, "", false},
		{"empty q field", "MEMO: type=daily_quiz | scored_q= | result=correct", map[int64]bool{}, 0, "", false},
		{"non-numeric q", "MEMO: type=daily_quiz | scored_q=one | result=correct", map[int64]bool{}, 0, "", false},
		{"no result field", "MEMO: type=daily_quiz | date=2026-08-04 | scored_q=1 | answered=1", map[int64]bool{}, 0, "", false},
		{"bad result", "MEMO: type=daily_quiz | scored_q=1 | result=maybe", map[int64]bool{}, 0, "", false},
		{"unknown id, two pending -> reject", "MEMO: type=daily_quiz | scored_q=99 | result=correct", map[int64]bool{}, 0, "", false},
		{"unknown id, one pending -> corrected", "MEMO: type=daily_quiz | scored_q=99 | result=correct", map[int64]bool{1: true}, 2, "correct", true},
		{"unknown id, none pending -> reject", "MEMO: type=daily_quiz | scored_q=99 | result=correct", map[int64]bool{1: true, 2: true}, 0, "", false},
		{"already reported id -> reject dup", "MEMO: type=daily_quiz | scored_q=1 | result=correct", map[int64]bool{1: true}, 0, "", false},
		{"story memo ignored", "MEMO: type=story | scored_q=1 | result=correct", map[int64]bool{}, 0, "", false},
		{"missing type ignored", "MEMO: q=1 | result=correct", map[int64]bool{}, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, res, ok := parseQuizVerdict(c.memo, batch, c.reported)
			if ok != c.wantOK || id != c.wantID || res != c.wantResult {
				t.Fatalf("got (%d,%q,%v) want (%d,%q,%v)", id, res, ok, c.wantID, c.wantResult, c.wantOK)
			}
		})
	}

	// nil batch = the fetch failed, so there is no scored quiz to report against
	if _, _, ok := parseQuizVerdict("MEMO: type=daily_quiz | scored_q=1 | result=correct", nil, map[int64]bool{}); ok {
		t.Fatal("nil batch must never report")
	}
	// an empty batch has no pending question to correct an id onto
	if _, _, ok := parseQuizVerdict("MEMO: type=daily_quiz | scored_q=1 | result=correct", &QuizBatch{}, map[int64]bool{}); ok {
		t.Fatal("empty batch must never report")
	}
	// a nil reported map (never-reported session) must behave like an empty one
	if id, res, ok := parseQuizVerdict("MEMO: type=daily_quiz | scored_q=2 | result=wrong", batch, nil); !ok || id != 2 || res != "wrong" {
		t.Fatalf("nil reported map: got (%d,%q,%v) want (2,\"wrong\",true)", id, res, ok)
	}
}

func TestMaybePersistQuizStatePerTypeFiles(t *testing.T) {
	dir := t.TempDir()

	// quiz memo -> memory/state/daily_quiz.md
	maybePersistQuizState(dir, "Correct!\nMEMO: type=daily_quiz | date=2026-08-01 | answered=1 | asked_keys=k1")
	quizPath := filepath.Join(dir, "memory", "state", "daily_quiz.md")
	data, err := os.ReadFile(quizPath)
	if err != nil || !strings.Contains(string(data), "answered=1") {
		t.Fatalf("quiz persist failed: %v %s", err, data)
	}

	// story memo -> its own file; quiz file untouched (the clobber bug)
	maybePersistQuizState(dir, "Achha suno.\nMEMO: type=story | date=2026-08-01 | story_key=moon_robot | beat=1_of_6 | completed=false")
	storyPath := filepath.Join(dir, "memory", "state", "story.md")
	if data, err = os.ReadFile(storyPath); err != nil || !strings.Contains(string(data), "moon_robot") {
		t.Fatalf("story persist failed: %v %s", err, data)
	}
	if data, _ = os.ReadFile(quizPath); !strings.Contains(string(data), "answered=1") {
		t.Fatalf("story memo clobbered quiz state: %s", data)
	}

	// second quiz write replaces, never appends
	maybePersistQuizState(dir, "Ting!\nMEMO: type=daily_quiz | date=2026-08-01 | answered=2 | asked_keys=k1,k2")
	data, _ = os.ReadFile(quizPath)
	if strings.Contains(string(data), "answered=1") || strings.Count(string(data), "MEMO:") != 1 {
		t.Fatalf("second write did not replace: %s", data)
	}

	// truncated memo (the live gemma failure) must not create or overwrite anything
	maybePersistQuizState(dir, "Nice story.\n\nMEMO: type=")
	if data, _ = os.ReadFile(quizPath); !strings.Contains(string(data), "answered=2") {
		t.Fatalf("truncated memo overwrote quiz state: %s", data)
	}

	// no memo -> no write; empty workspace -> no panic
	maybePersistQuizState(dir, "[happy] Question one!")
	maybePersistQuizState("", "MEMO: type=story | x=1")
	maybePersistQuizState("   ", "MEMO: type=story | x=1")
}

func TestLedgers(t *testing.T) {
	dir := t.TempDir()
	sdir := filepath.Join(dir, "memory", "state")

	// Dates are RELATIVE to now, never hardcoded: both ledgers prune by age
	// (story 30 days, quiz 14), so a literal date silently rots into a failure
	// once the calendar passes it. This test began failing on 2026-08-16 for
	// exactly that reason — its quiz date was 14 days old and the line it
	// asserted on was pruned the moment it was written.
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// incomplete story -> no ledger entry
	maybePersistQuizState(dir, "MEMO: type=story | date="+yesterday+" | story_key=moon_robot | title=Moon Robot | theme=sharing | completed=false")
	if _, err := os.Stat(filepath.Join(sdir, storyLedgerFile)); !os.IsNotExist(err) {
		t.Fatal("incomplete story wrote ledger")
	}

	// completed story -> ledger entry; re-persisting same key -> no duplicate
	done := "MEMO: type=story | date=" + yesterday + " | story_key=moon_robot | title=Moon Robot | theme=sharing | completed=true"
	maybePersistQuizState(dir, done)
	maybePersistQuizState(dir, done)
	data, err := os.ReadFile(filepath.Join(sdir, storyLedgerFile))
	if err != nil || strings.Count(string(data), "moon_robot") != 1 {
		t.Fatalf("story ledger wrong: %v %s", err, data)
	}

	// quiz ledger: same-date line upserted, not appended
	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date="+today+" | asked_keys=k1")
	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date="+today+" | asked_keys=k1,k2")
	data, err = os.ReadFile(filepath.Join(sdir, questionLedgerFile))
	if err != nil || strings.Count(string(data), today) != 1 || !strings.Contains(string(data), "k1,k2") {
		t.Fatalf("quiz ledger wrong: %v %s", err, data)
	}
}

// A ledger line older than its bank's window must be pruned, and one inside it
// kept. This is what the rotted dates above were accidentally testing; pinning
// it deliberately means the relative dates cannot drift back into that hole.
func TestLedgerPrunesByAge(t *testing.T) {
	dir := t.TempDir()
	sdir := filepath.Join(dir, "memory", "state")

	stale := time.Now().AddDate(0, 0, -20).Format("2006-01-02") // > 14d quiz window
	fresh := time.Now().AddDate(0, 0, -2).Format("2006-01-02")

	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date="+stale+" | asked_keys=old")
	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date="+fresh+" | asked_keys=new")

	data, err := os.ReadFile(filepath.Join(sdir, questionLedgerFile))
	if err != nil {
		t.Fatalf("no quiz ledger: %v", err)
	}
	if strings.Contains(string(data), "old") {
		t.Errorf("a line past the 14-day window survived: %s", data)
	}
	if !strings.Contains(string(data), "new") {
		t.Errorf("a line inside the window was pruned: %s", data)
	}
}

func TestMigrateLegacyQuizStateSection(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "# Memory\n\n## Session Summaries\n\n- summary A\n\n## Quiz State\n\n" +
		quizStateStartMarker + "\nMEMO: type=daily_quiz | date=2026-08-03 | answered=4\n" + quizStateEndMarker + "\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	MigrateLegacyQuizStateSection(dir)

	data, _ := os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if strings.Contains(string(data), "quiz-state") || strings.Contains(string(data), "MEMO:") {
		t.Fatalf("legacy section not removed: %s", data)
	}
	if !strings.Contains(string(data), "- summary A") {
		t.Fatalf("summaries clobbered: %s", data)
	}
	state, err := os.ReadFile(filepath.Join(memDir, "state", "daily_quiz.md"))
	if err != nil || !strings.Contains(string(state), "answered=4") {
		t.Fatalf("state file not created: %v %s", err, state)
	}

	// idempotent + truncated legacy content ("MEMO: type=") is dropped, not migrated
	MigrateLegacyQuizStateSection(dir)
	trunc := "# Memory\n\n" + quizStateStartMarker + "\nMEMO: type=\n" + quizStateEndMarker + "\n"
	os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(trunc), 0o600)
	MigrateLegacyQuizStateSection(dir)
	data, _ = os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if strings.Contains(string(data), "quiz-state") {
		t.Fatalf("truncated legacy section not cleaned: %s", data)
	}
}

func TestPruneStaleStateFiles(t *testing.T) {
	dir := t.TempDir()
	sdir := filepath.Join(dir, "memory", "state")
	os.MkdirAll(sdir, 0o755)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(sdir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("daily_quiz.md", "MEMO: type=daily_quiz | date=2026-08-10 | answered=1\n")                   // fresh -> kept
	write("story.md", "MEMO: type=story | date=2026-08-01 | completed=false\n")                        // stale -> removed
	write("nodate.md", "MEMO: type=nodate | answered=1\n")                                             // no date -> kept (fail-open)
	write(storyLedgerFile, "2026-06-01 | old_key | Old | theme\n2026-08-01 | new_key | New | theme\n") // 30d prune
	write(questionLedgerFile, "2026-07-20 | asked_keys=old\n2026-08-09 | asked_keys=new\n")            // 14d prune

	removed, err := PruneStaleStateFiles(dir, now)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v, want 1 nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(sdir, "story.md")); !os.IsNotExist(err) {
		t.Fatal("stale story.md kept")
	}
	if _, err := os.Stat(filepath.Join(sdir, "daily_quiz.md")); err != nil {
		t.Fatal("fresh quiz state removed")
	}
	if _, err := os.Stat(filepath.Join(sdir, "nodate.md")); err != nil {
		t.Fatal("undated state removed (should fail open)")
	}
	sl, _ := os.ReadFile(filepath.Join(sdir, storyLedgerFile))
	if strings.Contains(string(sl), "old_key") || !strings.Contains(string(sl), "new_key") {
		t.Fatalf("story ledger prune wrong: %s", sl)
	}
	ql, _ := os.ReadFile(filepath.Join(sdir, questionLedgerFile))
	if strings.Contains(string(ql), "old") || !strings.Contains(string(ql), "new") {
		t.Fatalf("question ledger prune wrong: %s", ql)
	}

	// missing dir -> no-op
	if removed, err := PruneStaleStateFiles(t.TempDir(), now); err != nil || removed != 0 {
		t.Fatalf("missing dir: removed=%d err=%v", removed, err)
	}
}

// --- Bank state file ---------------------------------------------------------

func TestWriteQuizBankStateSurvivesAsStateFile(t *testing.T) {
	ws := t.TempDir()
	batch := &QuizBatch{Level: 2, Band: "6-8", Questions: []QuizQuestion{
		{ID: 11, Text: "Which planet is closest to the sun?", Answer: "Mercury"},
		{ID: 12, Text: "What is nine times three?", Answer: "twenty-seven", Accepted: []string{"27"}},
	}}
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	if err := WriteQuizBankState(ws, batch, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir(ws), quizBankStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("bank state file not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{"date=2026-08-04", "id=11", "Which planet is closest", "id=12", "27"} {
		if !strings.Contains(got, want) {
			t.Errorf("bank state missing %q in:\n%s", want, got)
		}
	}
	// Must carry date= so PruneStaleStateFiles can age it out; without it the
	// file is kept forever and a stale list gets injected tomorrow.
	if quizMemoDateRE.FindStringSubmatch(got) == nil {
		t.Error("bank state has no parseable date=; prune would keep it forever")
	}
}

func TestWriteQuizBankStateRemovesFileWhenBatchUnavailable(t *testing.T) {
	ws := t.TempDir()
	now := time.Now()
	if err := WriteQuizBankState(ws, &QuizBatch{Level: 1, Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}}}, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir(ws), quizBankStateFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("expected file to exist first")
	}
	// A failed fetch must clear it: serving yesterday's list would let the child
	// be asked questions the server never selected.
	if err := WriteQuizBankState(ws, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("nil batch must remove the stale bank state file")
	}
	if err := WriteQuizBankState(ws, &QuizBatch{}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("empty batch must remove the stale bank state file")
	}
}

func TestPruneStaleStateFilesAgesOutBankFile(t *testing.T) {
	ws := t.TempDir()
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if err := WriteQuizBankState(ws, &QuizBatch{Level: 1, Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}}}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneStaleStateFiles(ws, base.Add(4*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir(ws), quizBankStateFile)); !os.IsNotExist(err) {
		t.Error("a four-day-old bank file should have been pruned")
	}
}

// --- Verdict attribution and the invented-question guard ---------------------

func TestParseQuizVerdictUsesScoredQNotAwaiting(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{
		{ID: 1, Text: "How many legs does a spider have?"},
		{ID: 2, Text: "What colour do you get when you mix red and yellow?"},
	}}
	// The live regression: the model scores question one while asking question
	// two in the same breath. Reporting awaiting= logged every answer one
	// question late.
	memo := "MEMO: type=daily_quiz | date=2026-08-04 | answered=1 | awaiting=2 | " +
		"scored_q=1 | scored_text=How many legs does a spider have | result=correct"
	id, result, ok := parseQuizVerdict(memo, batch, map[int64]bool{})
	if !ok || id != 1 || result != "correct" {
		t.Fatalf("got (%d,%q,%v), want (1,correct,true) — verdict must follow scored_q", id, result, ok)
	}
}

func TestParseQuizVerdictRejectsInventedQuestion(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{
		{ID: 7, Text: "What do we call water when it freezes and turns hard?"},
		{ID: 8, Text: "What do we call a baby dog?"},
	}}
	// Exactly what happened live: the list fell out of context, the model
	// invented a question and reported it against a real bank id.
	memo := "MEMO: type=daily_quiz | date=2026-08-04 | answered=6 | awaiting=8 | " +
		"scored_q=7 | scored_text=Which animal is known as the king of the jungle | result=correct"
	if _, _, ok := parseQuizVerdict(memo, batch, map[int64]bool{}); ok {
		t.Error("a scored_text that does not match the bank question must not be logged")
	}
}

func TestParseQuizVerdictAcceptsRewordedQuestion(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{
		{ID: 1, Text: "How many legs does a spider have?"},
	}}
	// Quizzy is told to ask warmly in her own words, so the guard must tolerate
	// padding and punctuation while still rejecting a different question.
	memo := "MEMO: type=daily_quiz | date=2026-08-04 | scored_q=1 | " +
		"scored_text=Can you tell me, how many legs does a spider have? | result=correct"
	if _, _, ok := parseQuizVerdict(memo, batch, map[int64]bool{}); !ok {
		t.Error("a warmly reworded version of the same question must still count")
	}
}

func TestParseQuizVerdictAllowsMissingScoredText(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{{ID: 3, Text: "Which planet do we live on?"}}}
	// Absent scored_text cannot be cross-checked; the id validation still
	// applies, so accept rather than silently dropping real answers.
	memo := "MEMO: type=daily_quiz | date=2026-08-04 | scored_q=3 | result=revealed"
	id, result, ok := parseQuizVerdict(memo, batch, map[int64]bool{})
	if !ok || id != 3 || result != "revealed" {
		t.Fatalf("got (%d,%q,%v), want (3,revealed,true)", id, result, ok)
	}
}

func TestQuestionTextMatchesBank(t *testing.T) {
	cases := []struct {
		name, asked, bank string
		want              bool
	}{
		{"identical", "What is five plus seven?", "What is five plus seven?", true},
		{"reworded warmly", "Ooh, can you tell me what is five plus seven?", "What is five plus seven?", true},
		{"case and punctuation", "WHAT IS FIVE PLUS SEVEN", "What is five plus seven?", true},
		{"different question", "Which animal is the king of the jungle?", "What do we call a baby dog?", false},
		{"the live giraffe invention", "Which animal has a very long neck to reach leaves?", "What do we call a baby dog?", false},
		{"empty asked", "", "What is five plus seven?", true},
		// Live false negative: the model abbreviates because the prompt asks it
		// to name the question in a few plain words.
		{"abbreviated by the model", "days in a week", "How many days are there in one week?", true},
		{"abbreviated, still wrong question", "king of the jungle", "What do we call water when it freezes and turns hard?", false},
		{"single distinctive word", "spider", "How many legs does a spider have?", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := questionTextMatchesBank(c.asked, c.bank); got != c.want {
				t.Errorf("questionTextMatchesBank(%q, %q) = %v, want %v", c.asked, c.bank, got, c.want)
			}
		})
	}
}

func TestQuestionTextMatchesBankAcceptsParaphrase(t *testing.T) {
	// Live false negative: the model summarised in its own words, so "number"
	// never appears in the bank text and a proportion rule dropped a real answer.
	if !questionTextMatchesBank("number of eyes", "How many eyes do you have?") {
		t.Error("a paraphrase sharing one distinctive word must be accepted")
	}
}

func TestVerdictMatchesClaimedQuestion(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{
		{ID: 1, Text: "Which animal says meow?"},
		{ID: 2, Text: "Which animal has a long trunk?"},
		{ID: 3, Text: "How many eyes do you have?"},
	}}
	cases := []struct {
		name, asked, claimed string
		want                 bool
	}{
		{"paraphrase of the claimed question", "number of eyes", "How many eyes do you have?", true},
		{"exact claimed question", "Which animal says meow?", "Which animal says meow?", true},
		{"invented question matches nothing", "king of the jungle", "How many eyes do you have?", false},
		// Both questions mention "animal"; the reported id must be the better fit.
		{"id points at the wrong question", "animal with a long trunk", "Which animal says meow?", false},
		{"weak but unambiguous overlap", "the meow animal", "Which animal says meow?", true},
		{"nothing to check", "", "How many eyes do you have?", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verdictMatchesClaimedQuestion(c.asked, c.claimed, batch); got != c.want {
				t.Errorf("verdictMatchesClaimedQuestion(%q, %q) = %v, want %v", c.asked, c.claimed, got, c.want)
			}
		})
	}
}

// TestScoredMemoTypesSeparateTheBanks pins the per-character split: each scored
// bank must score under its own type= label and keep its own asked_keys ledger.
// Sharing daily_quiz let Quizzy, Bujho and Ginti overwrite each other's daily
// scoreboard and resume at another bank's question id.
func TestScoredMemoTypesSeparateTheBanks(t *testing.T) {
	batch := &QuizBatch{Questions: []QuizQuestion{{ID: 1}, {ID: 2}}}

	// Every scored label scores. daily_quiz stays accepted so a prompt that has
	// not been migrated yet keeps recording verdicts.
	for _, typ := range []string{"daily_quiz", "daily_riddle", "daily_math"} {
		memo := "MEMO: type=" + typ + " | scored_q=1 | result=correct"
		if id, res, ok := parseQuizVerdict(memo, batch, map[int64]bool{}); !ok || id != 1 || res != "correct" {
			t.Errorf("%s: got (%d,%q,%v), want (1,\"correct\",true)", typ, id, res, ok)
		}
	}
	// An unscored type must not reach the answer log.
	if _, _, ok := parseQuizVerdict("MEMO: type=story | scored_q=1 | result=correct", batch, map[int64]bool{}); ok {
		t.Error("story memo scored; only scoredMemoTypes may reach the answer log")
	}

	// One ledger per bank, or the day's second bank overwrites the first's
	// asked_keys and the no-repeat promise stops holding.
	seen := map[string]string{}
	for typ := range scoredMemoTypes {
		name := questionLedgerFor(typ)
		if prev, dup := seen[name]; dup {
			t.Errorf("%s and %s share ledger %q", prev, typ, name)
		}
		seen[name] = typ
		if !isQuestionLedger(name) {
			t.Errorf("%s: ledger %q not recognised, so it would age out at the story window", typ, name)
		}
	}
	// Quizzy's existing fourteen days must survive the rename.
	if got := questionLedgerFor("daily_quiz"); got != questionLedgerFile {
		t.Errorf("daily_quiz ledger = %q, want %q — renaming it discards Quizzy's history", got, questionLedgerFile)
	}
}
