package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHydratedWorkspaceMemoryShape(t *testing.T) {
	workspace := t.TempDir()
	opts := liveKitWorkspaceHydrationOptions{
		MemoryContent:         "# Memory\n\n## Stable Memory\n\n- Rahul expects memory.\n\n## Recent Session Summaries\n\n- 2026-04-24 12:38 UTC, ended, 12 messages: Asked about octopuses.",
		SessionContextContent: "# Recent Voice Messages\n\n- 2026-04-24T12:39:00Z [s1] user: Do you remember yesterday?",
	}

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, opts)
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	memoryData, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	recentData, err := os.ReadFile(filepath.Join(workspace, "sessions", "manager_recent_voice_context.md"))
	if err != nil {
		t.Fatalf("read recent context: %v", err)
	}

	if strings.Contains(string(memoryData), "Recent Voice Messages") {
		t.Fatalf("MEMORY.md should not contain raw recent messages:\n%s", string(memoryData))
	}
	if !strings.Contains(string(recentData), "Recent Voice Messages") {
		t.Fatalf("recent context missing heading:\n%s", string(recentData))
	}
}
