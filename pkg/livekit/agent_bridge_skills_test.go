package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAgentBridgeBuildMessagesIncludesActiveSkills(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "skills", "weather")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(skillDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: weather
description: Use weather APIs before answering weather questions.
---
# Weather Skill

Always check live weather before answering weather questions.
`), 0o644); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}

	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			ContextBuilder: agent.NewContextBuilder(workspace),
			SkillsFilter:   []string{"weather"},
		},
		contextBuilder: agent.NewContextBuilder(workspace),
	}

	messages := bridge.buildMessages(nil, "", "what is the weather?", "livekit:device:a")
	var joined strings.Builder
	for _, msg := range messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}

	if !strings.Contains(joined.String(), "# Active Skills") {
		t.Fatalf("messages missing active skills section:\n%s", joined.String())
	}
	if !strings.Contains(joined.String(), "Always check live weather before answering weather questions.") {
		t.Fatalf("messages missing weather skill content:\n%s", joined.String())
	}
	if !strings.Contains(joined.String(), "curl.exe") {
		t.Fatalf("messages should tell Windows LiveKit agents to use curl.exe:\n%s", joined.String())
	}
}

func TestAgentBridgeBuildMessagesIncludesFreshnessPolicy(t *testing.T) {
	workspace := t.TempDir()
	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			ContextBuilder: agent.NewContextBuilder(workspace),
		},
		contextBuilder: agent.NewContextBuilder(workspace),
	}

	messages := bridge.buildMessages(nil, "", "get the latest team data", "livekit:device:a")
	var joined strings.Builder
	for _, msg := range messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}

	for _, want := range []string{
		"For current or time-sensitive facts",
		"do not answer from memory",
		"Do not use web_fetch on search result pages",
		"fetch a real source page",
		"say you could not verify it",
	} {
		if !strings.Contains(joined.String(), want) {
			t.Fatalf("messages missing freshness policy %q:\n%s", want, joined.String())
		}
	}
}

func TestAgentBridgeBuildMessagesIncludesSessionLanguageLock(t *testing.T) {
	workspace := t.TempDir()
	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			ContextBuilder: agent.NewContextBuilder(workspace),
		},
		contextBuilder:      agent.NewContextBuilder(workspace),
		sessionLanguageName: "Hindi",
		sessionLanguageCode: "hi-IN",
		languageLockEnabled: true,
	}

	messages := bridge.buildMessages(nil, "", "hello", "livekit:device:a")
	joined := joinMessages(messages)
	if !strings.Contains(joined, "Session Language Override") {
		t.Fatalf("missing language lock directive:\n%s", joined)
	}
	if !strings.Contains(joined, "Speak only in Hindi") {
		t.Fatalf("missing strict language lock text:\n%s", joined)
	}
	if strings.Index(joined, "Session Language Override") > strings.Index(joined, "## Voice Mode Active") {
		t.Fatalf("language lock should appear before voice mode directive:\n%s", joined)
	}
}

func TestAgentBridgeBuildMessagesSkipsSessionLanguageLockWhenDisabled(t *testing.T) {
	workspace := t.TempDir()
	bridge := &AgentBridge{
		agentInstance: &agent.AgentInstance{
			ContextBuilder: agent.NewContextBuilder(workspace),
		},
		contextBuilder:      agent.NewContextBuilder(workspace),
		sessionLanguageName: "Hindi",
		sessionLanguageCode: "hi-IN",
		languageLockEnabled: false,
	}
	joined := joinMessages(bridge.buildMessages(nil, "", "hello", "livekit:device:a"))
	if strings.Contains(joined, "Session Language Override") {
		t.Fatalf("language lock directive should be absent when disabled:\n%s", joined)
	}
}

func joinMessages(messages []providers.Message) string {
	var out strings.Builder
	for _, msg := range messages {
		out.WriteString(msg.Role)
		out.WriteString(":")
		out.WriteString(msg.Content)
		out.WriteString("\n")
	}
	return out.String()
}
