package agent

import (
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
