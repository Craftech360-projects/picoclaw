package livekit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func timeDaysAgo(days int) time.Time { return time.Now().Add(-time.Duration(days) * 24 * time.Hour) }
func timeHour() time.Duration        { return time.Hour }

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

	memos := CollectStateMemos(ws)
	got := map[string]bool{}
	for _, m := range memos {
		got[m.Type] = true
	}
	if len(memos) != 2 || !got["spell_bee"] || !got["daily_quiz"] {
		t.Errorf("collected %v, want exactly spell_bee and daily_quiz", memos)
	}

	// With a session marker, only files touched AFTER it travel: a restored
	// ladder from another character's session must not be re-uploaded.
	marker := filepath.Join(dir, sessionMarkerFile)
	if err := os.WriteFile(marker, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := timeDaysAgo(3)
	if err := os.Chtimes(filepath.Join(dir, "spell_bee.md"), past, past); err != nil {
		t.Fatal(err)
	}
	future := timeDaysAgo(0).Add(timeHour())
	if err := os.Chtimes(filepath.Join(dir, "daily_quiz.md"), future, future); err != nil {
		t.Fatal(err)
	}
	memos = CollectStateMemos(ws)
	if len(memos) != 1 || memos[0].Type != "daily_quiz" {
		t.Errorf("marker filter: collected %v, want only daily_quiz", memos)
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
