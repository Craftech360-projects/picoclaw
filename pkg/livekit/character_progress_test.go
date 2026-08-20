package livekit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Progress travels as MEMO lines: only real MEMOs go up, and the restore only
// fills gaps — a local file is always fresher than the last session's upload.

func TestCollectStateMemosOnlyTakesMemoFiles(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, "memory", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{
		"spell_bee.md":      "MEMO: type=spell_bee | date=2026-08-20 | current_level=4",
		"quiz_bank.md":      "QUIZ_BANK: type=quiz_bank | date=2026-08-20 | bank=math",
		"story_ledger.md":   "2026-08-19 | jackal_and_drum | Golu and the Dhol | courage",
		"daily_quiz.md":     "MEMO: type=daily_quiz | date=2026-08-20 | answered=3",
		"empty_fragment.md": "MEMO: type=",
	}
	for name, content := range writes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Both types written this session: ledgers, the bank file and the truncated
	// fragment still stay home.
	all := map[string]bool{"spell_bee": true, "daily_quiz": true}
	memos := CollectStateMemos(ws, all)
	got := map[string]bool{}
	for _, m := range memos {
		got[m.Type] = true
	}
	if len(memos) != 2 || !got["spell_bee"] || !got["daily_quiz"] {
		t.Errorf("collected %v, want exactly spell_bee and daily_quiz", memos)
	}

	// The 2026-08-20 bug: the workspace is per-CHILD, so this directory holds
	// every character the child has played. A Quizzy session collected all of
	// them and relabelled six other characters' state as its own. Only what
	// this session wrote may travel.
	memos = CollectStateMemos(ws, map[string]bool{"daily_quiz": true})
	if len(memos) != 1 || memos[0].Type != "daily_quiz" {
		t.Errorf("collected %v, want only daily_quiz — spell_bee belongs to Tikku", memos)
	}

	// A session that persisted no MEMO reports nothing. Falling back to the
	// directory here is exactly what caused the mislabelling.
	if memos = CollectStateMemos(ws, nil); len(memos) != 0 {
		t.Errorf("collected %v with an empty written set, want none", memos)
	}
}

func TestRestoreCharacterStateFillsOnlyMissingFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"msg":"ok","data":{"states":[` +
			`{"state_type":"spell_bee","memo":"MEMO: type=spell_bee | current_level=7"},` +
			`{"state_type":"daily_jokes","memo":"MEMO: type=daily_jokes | jokes_told=MJ-01-01"},` +
			`{"state_type":"../evil","memo":"MEMO: type=x"}]}}`))
	}))
	defer srv.Close()

	ws := t.TempDir()
	dir := filepath.Join(ws, "memory", "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Local spell_bee already exists (fresher) — must NOT be overwritten.
	local := "MEMO: type=spell_bee | current_level=9\n"
	if err := os.WriteFile(filepath.Join(dir, "spell_bee.md"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}
	RestoreCharacterState(context.Background(), cfg, "key", "aa:bb:cc:dd:ee:ff", ws)

	if data, _ := os.ReadFile(filepath.Join(dir, "spell_bee.md")); string(data) != local {
		t.Errorf("local file was clobbered: %q", string(data))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "daily_jokes.md")); err != nil || len(data) == 0 {
		t.Errorf("missing state was not restored: %v", err)
	}
	// Path-traversal type names never become files.
	if _, err := os.Stat(filepath.Join(ws, "memory", "evil.md")); err == nil {
		t.Error("traversal state_type escaped the state dir")
	}
}
