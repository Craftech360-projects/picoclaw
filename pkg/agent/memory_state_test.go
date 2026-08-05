package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStateFilesInjection(t *testing.T) {
	ws := t.TempDir()
	ms := NewMemoryStore(ws)

	// no state dir -> empty, and GetMemoryContext has no Saved State section
	if got := ms.ReadStateFiles(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	stateDir := filepath.Join(ws, "memory", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stateDir, "daily_quiz.md"), []byte("MEMO: type=daily_quiz | answered=3\n"), 0o600)
	os.WriteFile(filepath.Join(stateDir, "story.md"), []byte("MEMO: type=story | beat=2_of_6\n"), 0o600)
	os.WriteFile(filepath.Join(stateDir, "empty.md"), []byte("   \n"), 0o600)   // blank -> skipped
	os.WriteFile(filepath.Join(stateDir, "notes.txt"), []byte("ignore"), 0o600) // non-md -> skipped

	out := ms.ReadStateFiles()
	if !strings.Contains(out, "### daily_quiz.md") || !strings.Contains(out, "answered=3") {
		t.Fatalf("quiz state missing: %q", out)
	}
	if !strings.Contains(out, "### story.md") || !strings.Contains(out, "beat=2_of_6") {
		t.Fatalf("story state missing: %q", out)
	}
	if strings.Contains(out, "empty.md") || strings.Contains(out, "notes.txt") {
		t.Fatalf("skipped files leaked: %q", out)
	}

	ctx := ms.GetMemoryContext()
	if !strings.Contains(ctx, "## Saved State") || !strings.Contains(ctx, "beat=2_of_6") {
		t.Fatalf("GetMemoryContext missing state block: %q", ctx)
	}
}

func TestCapSessionSummariesKeepsStableAndRecent(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# Long-term Memory\n\n## Stable Memory\n\n- likes cricket\n- name is Kishore\n\n## Session Summaries\n\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&sb, "- 2026-08-%02d: summary number %d\n", i%28+1, i)
	}
	out := capSessionSummaries(sb.String(), 10)

	if !strings.Contains(out, "- likes cricket") || !strings.Contains(out, "name is Kishore") {
		t.Fatal("stable memory must survive the cap")
	}
	if strings.Contains(out, "summary number 20") {
		t.Error("older summaries beyond the cap must be dropped")
	}
	for i := 21; i <= 30; i++ {
		if !strings.Contains(out, fmt.Sprintf("summary number %d", i)) {
			t.Errorf("recent summary %d must be kept", i)
		}
	}

	// Files without the section pass through untouched.
	plain := "# Notes\n\n- a\n- b\n"
	if capSessionSummaries(plain, 10) != plain {
		t.Error("content without a Session Summaries section must be unchanged")
	}
	// Under the cap: unchanged.
	small := "## Session Summaries\n\n- one\n- two\n"
	if capSessionSummaries(small, 10) != small {
		t.Error("a section under the cap must be unchanged")
	}
}

func TestSavedStateRendersInDynamicNotStatic(t *testing.T) {
	ws := t.TempDir()
	stateDir := filepath.Join(ws, "memory", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memo := "MEMO: type=daily_quiz | date=2026-08-05 | answered=3\n"
	if err := os.WriteFile(filepath.Join(stateDir, "daily_quiz.md"), []byte(memo), 0o600); err != nil {
		t.Fatal(err)
	}

	cb := NewContextBuilder(ws)
	static1 := cb.BuildSystemPromptWithCache()
	if strings.Contains(static1, "Saved State") || strings.Contains(static1, "answered=3") {
		t.Fatal("Saved State must not be in the cached static prompt")
	}

	dynamic := cb.buildDynamicContext("livekit", "room1", "", "")
	if !strings.Contains(dynamic, "## Saved State") || !strings.Contains(dynamic, "answered=3") {
		t.Fatalf("Saved State must render in the dynamic context, got:\n%s", dynamic)
	}

	// The regression this guards: a state-file write must NOT change the
	// static prompt. Before this change the prefix drifted every turn and the
	// provider KV cache never hit.
	if err := os.WriteFile(filepath.Join(stateDir, "daily_quiz.md"),
		[]byte("MEMO: type=daily_quiz | date=2026-08-05 | answered=4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if static2 := cb.BuildSystemPromptWithCache(); static2 != static1 {
		t.Fatal("static prompt must stay byte-identical across state-file writes")
	}
	if dyn2 := cb.buildDynamicContext("livekit", "room1", "", ""); !strings.Contains(dyn2, "answered=4") {
		t.Fatal("dynamic context must pick up the new state immediately")
	}
}
