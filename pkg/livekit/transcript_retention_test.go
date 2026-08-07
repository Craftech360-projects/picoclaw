package livekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

// backdateSession rewrites the session's last-activity stamp so the retention
// window can be crossed without waiting for it.
func backdateSession(t *testing.T, dir string, to time.Time) {
	t.Helper()
	metas, err := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	if err != nil || len(metas) != 1 {
		t.Fatalf("expected exactly one meta file in %s, got %v (%v)", dir, metas, err)
	}
	raw, err := os.ReadFile(metas[0])
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta["updated_at"] = to.Format(time.RFC3339Nano)
	patched, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metas[0], patched, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpireStaleTranscript(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const key = "livekit:device:aabbccddeeff"
	sessions := session.NewJSONLBackend(store)
	sessions.AddMessage(key, "user", "hello")
	sessions.AddMessage(key, "assistant", "hi there")
	sessions.SetSummary(key, "child likes trains")

	bridge := &AgentBridge{sessions: sessions}

	// A reconnect inside the window keeps its context — the one failure mode the
	// window exists to protect against.
	bridge.ExpireStaleTranscript(key)
	if got := len(sessions.GetHistory(key)); got != 2 {
		t.Fatalf("recent session lost history: %d messages, want 2", got)
	}

	backdateSession(t, dir, time.Now().Add(-transcriptRetention-time.Minute))
	bridge.ExpireStaleTranscript(key)

	if got := len(sessions.GetHistory(key)); got != 0 {
		t.Fatalf("stale transcript replayed: %d messages, want 0", got)
	}
	// Durable memory must survive: the summary is what Cheeko personalises from.
	if got := sessions.GetSummary(key); got != "child likes trains" {
		t.Fatalf("summary lost with the transcript: %q", got)
	}
}

// The legacy JSON store cannot report last activity; an unknown timestamp must
// read as "not stale", never as a reason to wipe.
func TestExpireStaleTranscriptNoOpWithoutLastActivity(t *testing.T) {
	const key = "livekit:device:aabbccddeeff"
	sessions := session.NewSessionManager("")
	sessions.AddMessage(key, "user", "hello")

	bridge := &AgentBridge{sessions: sessions}
	bridge.ExpireStaleTranscript(key)

	if got := len(sessions.GetHistory(key)); got != 1 {
		t.Fatalf("store without last-activity support was reset: %d messages, want 1", got)
	}
}
