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

	// incomplete story -> no ledger entry
	maybePersistQuizState(dir, "MEMO: type=story | date=2026-08-01 | story_key=moon_robot | title=Moon Robot | theme=sharing | completed=false")
	if _, err := os.Stat(filepath.Join(sdir, storyLedgerFile)); !os.IsNotExist(err) {
		t.Fatal("incomplete story wrote ledger")
	}

	// completed story -> ledger entry; re-persisting same key -> no duplicate
	done := "MEMO: type=story | date=2026-08-01 | story_key=moon_robot | title=Moon Robot | theme=sharing | completed=true"
	maybePersistQuizState(dir, done)
	maybePersistQuizState(dir, done)
	data, err := os.ReadFile(filepath.Join(sdir, storyLedgerFile))
	if err != nil || strings.Count(string(data), "moon_robot") != 1 {
		t.Fatalf("story ledger wrong: %v %s", err, data)
	}

	// quiz ledger: same-date line upserted, not appended
	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date=2026-08-02 | asked_keys=k1")
	maybePersistQuizState(dir, "MEMO: type=daily_quiz | date=2026-08-02 | asked_keys=k1,k2")
	data, err = os.ReadFile(filepath.Join(sdir, questionLedgerFile))
	if err != nil || strings.Count(string(data), "2026-08-02") != 1 || !strings.Contains(string(data), "k1,k2") {
		t.Fatalf("quiz ledger wrong: %v %s", err, data)
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
