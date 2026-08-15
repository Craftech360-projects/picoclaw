package livekit

import (
	"os"
	"path/filepath"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestSessionKeyForParticipantPrefersDeviceMAC(t *testing.T) {
	rs := &RoomSession{
		roomInfo:   &lkproto.Room{Name: "room-a"},
		deviceMAC:  "aa:11:bb:22:cc:33",
		agentID:    "agent-7",
		agentName:  "test-agent",
		sampleRate: 24000,
	}

	got := rs.sessionKeyForParticipant("participant-a")
	want := "livekit:device:aa11bb22cc33:agent:agent-7"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}

// A dispatch without a character must key exactly where it always has, or every
// deployment that has not shipped character_id yet loses its transcript.
func TestSessionKeyForParticipantWithoutAgentIDIsUnchanged(t *testing.T) {
	rs := &RoomSession{
		roomInfo:  &lkproto.Room{Name: "room-a"},
		deviceMAC: "aa:11:bb:22:cc:33",
	}

	got := rs.sessionKeyForParticipant("participant-a")
	want := "livekit:device:aa11bb22cc33"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}

func TestSessionKeyForParticipantSeparatesCharactersOnOneDevice(t *testing.T) {
	newSession := func(agentID string) *RoomSession {
		return &RoomSession{
			roomInfo:  &lkproto.Room{Name: "room-a"},
			deviceMAC: "aa:11:bb:22:cc:33",
			agentID:   agentID,
		}
	}

	cheeko := newSession("11111111-1111-1111-1111-111111111111")
	quizzy := newSession("22222222-2222-2222-2222-222222222222")

	if cheeko.sessionKeyForParticipant("p") == quizzy.sessionKeyForParticipant("p") {
		t.Fatalf("two characters on one device share a session key: %q",
			cheeko.sessionKeyForParticipant("p"))
	}
}

// The keys are only worth having if the store actually splits on them: two
// characters, two files, and neither one replaying the other's turns.
func TestTwoCharactersGetSeparateTranscriptFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewJSONLBackend(store)

	cheeko := (&RoomSession{deviceMAC: "aa:11:bb:22:cc:33", agentID: "cheeko-id"}).
		sessionKeyForParticipant("p")
	quizzy := (&RoomSession{deviceMAC: "aa:11:bb:22:cc:33", agentID: "quizzy-id"}).
		sessionKeyForParticipant("p")

	sessions.AddMessage(cheeko, "user", "tell me a story")
	sessions.AddMessage(quizzy, "user", "start the quiz")

	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected one transcript per character, got %v", files)
	}
	if got := sessions.GetHistory(quizzy); len(got) != 1 || got[0].Content != "start the quiz" {
		t.Fatalf("quizzy replayed cheeko's turns: %v", got)
	}
}

// The pre-003 device-wide file is read by nobody once the key carries a
// character; left on disk it rides along in every workspace sync.
func TestDiscardLegacyTranscriptRemovesTheOrphanedDeviceFile(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewJSONLBackend(store)

	rs := &RoomSession{
		roomInfo:  &lkproto.Room{Name: "room-a"},
		deviceMAC: "aa:11:bb:22:cc:33",
		agentID:   "cheeko-id",
		bridge:    &AgentBridge{sessions: sessions},
	}
	current := rs.sessionKeyForParticipant("p")

	sessions.AddMessage("livekit:device:aa11bb22cc33", "user", "from before the split")
	sessions.AddMessage(current, "user", "after the split")

	rs.discardLegacyTranscript(current)

	for _, name := range []string{
		"livekit_device_aa11bb22cc33.jsonl",
		"livekit_device_aa11bb22cc33.meta.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("orphaned %s survived (err=%v)", name, err)
		}
	}
	if got := sessions.GetHistory(current); len(got) != 1 {
		t.Fatalf("current transcript was collateral damage: %v", got)
	}
}

// Without a character the key is the legacy one, so there is nothing to orphan
// and the live transcript must not be deleted out from under the session.
func TestDiscardLegacyTranscriptKeepsTheFileWhenTheKeyIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewJSONLBackend(store)

	rs := &RoomSession{
		roomInfo:  &lkproto.Room{Name: "room-a"},
		deviceMAC: "aa:11:bb:22:cc:33",
		bridge:    &AgentBridge{sessions: sessions},
	}
	current := rs.sessionKeyForParticipant("p")
	sessions.AddMessage(current, "user", "hello")

	rs.discardLegacyTranscript(current)

	if got := sessions.GetHistory(current); len(got) != 1 {
		t.Fatalf("transcript deleted on a dispatch without a character: %v", got)
	}
}

func TestSessionKeyForParticipantFallsBackToAgentID(t *testing.T) {
	rs := &RoomSession{
		roomInfo: &lkproto.Room{Name: "room-b"},
		agentID:  "agent 42",
	}

	got := rs.sessionKeyForParticipant("participant-b")
	want := "livekit:agent:agent-42"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}

func TestSessionKeyForParticipantFallsBackToRoomAndIdentity(t *testing.T) {
	rs := &RoomSession{
		roomInfo: &lkproto.Room{Name: "room-c"},
	}

	got := rs.sessionKeyForParticipant("participant-c")
	want := "livekit:room-c:participant-c"
	if got != want {
		t.Fatalf("sessionKeyForParticipant() = %q, want %q", got, want)
	}
}
