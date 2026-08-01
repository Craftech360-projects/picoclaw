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

func TestUpsertQuizStateSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	// 1. fresh file (does not exist yet)
	if err := upsertQuizStateSection(path, "MEMO: date=2026-08-01 | answered=1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), quizStateStartMarker) ||
		!strings.Contains(string(data), "MEMO: date=2026-08-01 | answered=1") {
		t.Fatalf("fresh upsert missing section: %s", data)
	}

	// 2. replace existing section, preserve surrounding content
	seed := "# Memory\n\n## Session Summaries\n\n- summary A\n\n## Quiz State\n\n" +
		quizStateStartMarker + "\nMEMO: date=2026-08-01 | answered=1\n" + quizStateEndMarker + "\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertQuizStateSection(path, "MEMO: date=2026-08-01 | answered=2"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	s := string(data)
	if strings.Contains(s, "answered=1") || !strings.Contains(s, "answered=2") {
		t.Fatalf("replace failed: %s", s)
	}
	if !strings.Contains(s, "- summary A") {
		t.Fatalf("clobbered summaries: %s", s)
	}
	if strings.Count(s, quizStateStartMarker) != 1 || strings.Count(s, quizStateHeading) != 1 {
		t.Fatalf("duplicated section: %s", s)
	}

	// 3. corrupt remnant (truncation ate the start marker) -> self-heal, single section
	corrupt := "# Memory\n\nMEMO: date=old | garbage\n" + quizStateEndMarker + "\n"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertQuizStateSection(path, "MEMO: date=2026-08-01 | answered=3"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	s = string(data)
	if strings.Count(s, quizStateStartMarker) != 1 || !strings.Contains(s, "answered=3") {
		t.Fatalf("self-heal failed: %s", s)
	}
	if strings.Contains(s, "date=old") {
		t.Fatalf("orphaned old memo survived: %s", s)
	}

	// 4. empty memo line -> no-op, does not damage the file
	before, _ := os.ReadFile(path)
	if err := upsertQuizStateSection(path, "   "); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("empty memo modified file")
	}
}

func TestPruneStaleQuizState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	write := func(memo string) {
		content := "# Memory\n\n## Quiz State\n\n" + quizStateStartMarker + "\n" + memo + "\n" + quizStateEndMarker + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// fresh (same day) -> kept
	write("MEMO: type=daily_quiz | date=2026-08-03 | answered=1")
	removed, err := PruneStaleQuizState(path, now)
	if err != nil || removed {
		t.Fatalf("fresh pruned: removed=%v err=%v", removed, err)
	}

	// yesterday (<48h) -> kept, prompt handles new-day logic
	write("MEMO: type=daily_quiz | date=2026-08-02 | answered=5")
	removed, err = PruneStaleQuizState(path, now)
	if err != nil || removed {
		t.Fatalf("yesterday pruned: removed=%v err=%v", removed, err)
	}

	// stale (>48h) -> removed, markers gone
	write("MEMO: type=daily_quiz | date=2026-07-30 | answered=9")
	removed, err = PruneStaleQuizState(path, now)
	if err != nil || !removed {
		t.Fatalf("stale kept: removed=%v err=%v", removed, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "quiz-state") || strings.Contains(string(data), "MEMO:") {
		t.Fatalf("stale section not fully removed: %s", data)
	}

	// unparseable date -> kept (fail-open)
	write("MEMO: type=daily_quiz | date=banana | answered=1")
	removed, _ = PruneStaleQuizState(path, now)
	if removed {
		t.Fatal("unparseable date should be kept")
	}

	// no section -> no-op
	if err := os.WriteFile(path, []byte("# Memory\n\n- note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err = PruneStaleQuizState(path, now)
	if err != nil || removed {
		t.Fatalf("no-section file: removed=%v err=%v", removed, err)
	}

	// no file -> no-op, no error
	os.Remove(path)
	if removed, err = PruneStaleQuizState(path, now); err != nil || removed {
		t.Fatalf("missing file: removed=%v err=%v", removed, err)
	}
}

func TestMaybePersistQuizState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}

	// content without MEMO -> no file created
	maybePersistQuizState(dir, "[happy] Question one!")
	if _, err := os.Stat(filepath.Join(dir, "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatal("wrote file without MEMO")
	}

	// content with MEMO -> file created with section
	maybePersistQuizState(dir, "Correct!\nMEMO: date=2026-08-01 | answered=1")
	data, err := os.ReadFile(filepath.Join(dir, "memory", "MEMORY.md"))
	if err != nil || !strings.Contains(string(data), "answered=1") {
		t.Fatalf("persist failed: %v %s", err, data)
	}

	// second write replaces, never appends
	maybePersistQuizState(dir, "Ting!\nMEMO: date=2026-08-01 | answered=2")
	data, _ = os.ReadFile(filepath.Join(dir, "memory", "MEMORY.md"))
	if strings.Contains(string(data), "answered=1") || strings.Count(string(data), "MEMO:") != 1 {
		t.Fatalf("second write did not replace: %s", data)
	}

	// empty workspace -> must not panic
	maybePersistQuizState("", "MEMO: x")
	maybePersistQuizState("   ", "MEMO: x")
}
