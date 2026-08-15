package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"

	"github.com/sipeed/picoclaw/pkg/agent"
)

// Splitting the transcript per character must not split the summaries: MEMORY.md
// is the shared-continuity half of the decision, so Cheeko still learns the child
// had a quiz this morning without replaying Quizzy's turns.
func TestSessionSummariesStayInOneSharedMemoryFile(t *testing.T) {
	workspace := t.TempDir()
	rs := &RoomSession{roomInfo: &lkproto.Room{Name: "room-a"}}

	for _, c := range []struct{ character, summary string }{
		{"Cheeko", "talked about trains"},
		{"Quizzy", "answered five questions"},
	} {
		bridge := &AgentBridge{
			agentInstance: &agent.AgentInstance{Workspace: workspace},
			characterName: c.character,
		}
		if err := rs.persistSummaryToMemoryFile(bridge, c.summary, 9); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"[Cheeko]", "talked about trains", "[Quizzy]", "answered five questions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("MEMORY.md missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "## Session Summaries"); n != 1 {
		t.Fatalf("expected one shared Session Summaries section, got %d:\n%s", n, got)
	}
}
