package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLiveKitWorkspaceTemplateFromSourcesSeedsMissingWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace-target")
	template := t.TempDir()

	mustWriteFile(t, filepath.Join(template, "AGENT.md"), "# Agent\n\nTemplate.\n")
	mustWriteFile(t, filepath.Join(template, "SOUL.md"), "# Soul\n\nTemplate.\n")
	mustWriteFile(t, filepath.Join(template, "USER.md"), "# User\n\nTemplate.\n")
	mustWriteFile(t, filepath.Join(template, "memory", "MEMORY.md"), "# Memory\n\nTemplate.\n")
	mustWriteFile(t, filepath.Join(template, "skills", "weather", "SKILL.md"), "# Weather\n\nTemplate weather.\n")

	result, err := ensureLiveKitWorkspaceTemplateFromSources(
		workspace,
		[]string{workspace, template},
		[]string{filepath.Join(workspace, "skills"), filepath.Join(template, "skills")},
	)
	if err != nil {
		t.Fatalf("ensureLiveKitWorkspaceTemplateFromSources returned error: %v", err)
	}
	if result.SeededFiles != 4 {
		t.Fatalf("SeededFiles = %d, want 4", result.SeededFiles)
	}
	if result.SkillsCopied != 1 {
		t.Fatalf("SkillsCopied = %d, want 1", result.SkillsCopied)
	}

	for _, rel := range []string{
		"AGENT.md",
		"SOUL.md",
		"USER.md",
		"memory/MEMORY.md",
		"skills/weather/SKILL.md",
	} {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}

func TestEnsureLiveKitWorkspaceTemplateFromSourcesFailsWhenNoTemplateExists(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace-target")
	_, err := ensureLiveKitWorkspaceTemplateFromSources(
		workspace,
		[]string{workspace},
		[]string{filepath.Join(workspace, "skills")},
	)
	if err == nil {
		t.Fatal("expected error when no template sources are available")
	}
	if !strings.Contains(err.Error(), "default workspace template is incomplete") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureLiveKitWorkspaceTemplateFromSourcesDoesNotOverwriteExistingSkills(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace-target")
	template := t.TempDir()

	mustWriteFile(t, filepath.Join(workspace, "AGENT.md"), "# Agent\n\nExisting.\n")
	mustWriteFile(t, filepath.Join(workspace, "SOUL.md"), "# Soul\n\nExisting.\n")
	mustWriteFile(t, filepath.Join(workspace, "USER.md"), "# User\n\nExisting.\n")
	mustWriteFile(t, filepath.Join(workspace, "memory", "MEMORY.md"), "# Memory\n\nExisting.\n")
	mustWriteFile(t, filepath.Join(workspace, "skills", "weather", "SKILL.md"), "# Weather\n\nExisting weather.\n")

	mustWriteFile(t, filepath.Join(template, "skills", "weather", "SKILL.md"), "# Weather\n\nTemplate weather.\n")
	mustWriteFile(t, filepath.Join(template, "skills", "agent-browser", "SKILL.md"), "# Browser\n\nTemplate browser.\n")

	result, err := ensureLiveKitWorkspaceTemplateFromSources(
		workspace,
		[]string{workspace, template},
		[]string{filepath.Join(workspace, "skills"), filepath.Join(template, "skills")},
	)
	if err != nil {
		t.Fatalf("ensureLiveKitWorkspaceTemplateFromSources returned error: %v", err)
	}
	if result.ExistingSkills == 0 {
		t.Fatalf("ExistingSkills = %d, want > 0", result.ExistingSkills)
	}
	if result.SkillsCopied != 0 {
		t.Fatalf("SkillsCopied = %d, want 0 for existing workspace skills", result.SkillsCopied)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "skills", "weather", "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile(existing skill) error = %v", err)
	}
	if !strings.Contains(string(data), "Existing weather") {
		t.Fatalf("existing skill was overwritten: %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "agent-browser", "SKILL.md")); err == nil {
		t.Fatal("agent-browser should not be copied when workspace already has skills")
	}
}
