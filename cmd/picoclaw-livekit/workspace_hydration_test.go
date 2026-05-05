package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHydrateLiveKitWorkspaceSkeletonCreatesPromptAdvertisedPaths(t *testing.T) {
	workspace := t.TempDir()
	sourceWorkspace := t.TempDir()
	sourceSkill := filepath.Join(sourceWorkspace, "skills", "song-reader", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(sourceSkill), 0o755); err != nil {
		t.Fatalf("MkdirAll(source skill) error = %v", err)
	}
	if err := os.WriteFile(sourceSkill, []byte("# Song Reader\n\nRead songs aloud."), 0o644); err != nil {
		t.Fatalf("WriteFile(source skill) error = %v", err)
	}

	result, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		IdentityContent: "# Identity\n\nRahul is the active child.",
		UserContent:     "# User\n\nName: Rahul",
		MemoryContent:   "- Name: Rahul\n- Age: 10",
		SkillsSourceDir: filepath.Join(sourceWorkspace, "skills"),
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}
	if !result.MemoryWritten {
		t.Fatal("MemoryWritten = false, want true")
	}
	if result.SkillsCopied != 1 {
		t.Fatalf("SkillsCopied = %d, want 1", result.SkillsCopied)
	}

	for _, rel := range []string{
		"memory",
		"sessions",
		"skills",
		"cron",
		"state",
	} {
		info, err := os.Stat(filepath.Join(workspace, rel))
		if err != nil {
			t.Fatalf("expected directory %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", rel)
		}
	}

	for _, rel := range []string{
		"AGENT.md",
		"USER.md",
		"SOUL.md",
		"HEARTBEAT.md",
		"heartbeat.log",
		"memory/MEMORY.md",
		"skills/song-reader/SKILL.md",
	} {
		info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("expected file %s: %v", rel, err)
		}
		if info.IsDir() {
			t.Fatalf("%s is a directory, want file", rel)
		}
	}

	agentContent, err := os.ReadFile(filepath.Join(workspace, "AGENT.md"))
	if err != nil {
		t.Fatalf("ReadFile(AGENT.md) error = %v", err)
	}
	if !strings.Contains(string(agentContent), "Rahul is the active child") {
		t.Fatalf("AGENT.md should include rendered identity, got %q", string(agentContent))
	}

	memoryContent, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("ReadFile(MEMORY.md) error = %v", err)
	}
	if !strings.Contains(string(memoryContent), "Age: 10") {
		t.Fatalf("MEMORY.md should include hydrated memory, got %q", string(memoryContent))
	}

	userContent, err := os.ReadFile(filepath.Join(workspace, "USER.md"))
	if err != nil {
		t.Fatalf("ReadFile(USER.md) error = %v", err)
	}
	if !strings.Contains(string(userContent), "Name: Rahul") {
		t.Fatalf("USER.md should include hydrated user context, got %q", string(userContent))
	}
}

func TestHydrateLiveKitWorkspaceSkeletonDoesNotOverwriteMemoryWithEmptyPlaceholder(t *testing.T) {
	workspace := t.TempDir()
	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(memory) error = %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("# Memory\n\nExisting durable memory."), 0o600); err != nil {
		t.Fatalf("WriteFile(memory) error = %v", err)
	}

	result, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}
	if result.MemoryWritten {
		t.Fatal("MemoryWritten = true, want false for existing memory with empty hydration content")
	}

	data, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("ReadFile(memory) error = %v", err)
	}
	if string(data) != "# Memory\n\nExisting durable memory." {
		t.Fatalf("memory was overwritten: %q", string(data))
	}
}

func TestHydrateLiveKitWorkspaceWritesManagerSessionContext(t *testing.T) {
	workspace := t.TempDir()

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		SessionContextContent: "# Recent Voice Messages\n\n- user: hello from yesterday",
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	path := filepath.Join(workspace, "sessions", "manager_recent_voice_context.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile session context error = %v", err)
	}
	if !strings.Contains(string(data), "hello from yesterday") {
		t.Fatalf("session context file missing restored message: %q", string(data))
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("session context file should end with newline: %q", string(data))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat session context error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("session context mode = %v, want 0600", got)
	}
}

func TestHydrateLiveKitWorkspaceSkeletonDoesNotOverwriteAgentWithEmptyIdentity(t *testing.T) {
	workspace := t.TempDir()
	agentPath := filepath.Join(workspace, "AGENT.md")
	if err := os.WriteFile(agentPath, []byte("# Agent\n\nExisting custom identity."), 0o644); err != nil {
		t.Fatalf("WriteFile(AGENT.md) error = %v", err)
	}

	if _, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{}); err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	data, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("ReadFile(AGENT.md) error = %v", err)
	}
	if string(data) != "# Agent\n\nExisting custom identity." {
		t.Fatalf("AGENT.md was overwritten: %q", string(data))
	}
}

func TestBuildLiveKitWorkspaceHydrationOptionsUsesRoomMetadata(t *testing.T) {
	baseWorkspace := filepath.Join(t.TempDir(), "workspace")
	bootstrap := roomMetadataBootstrap{
		Source: bootstrapSourceRoomMetadata,
		Metadata: roomMetadata{
			ChildProfile: roomMetadataChildProfile{
				Name:      "Rahul",
				Age:       10,
				Gender:    "boy",
				Interests: "flowers",
			},
			LongTermMemories: []string{"Rahul likes music"},
			MemoryRelations: []roomMetadataRelation{
				{Source: "Rahul", Relation: "likes", Target: "sunflowers"},
			},
			MemoryEntities: []roomMetadataEntity{
				{Name: "Cheeko", Type: "assistant"},
			},
			PrimaryLanguage: "Hindi",
			AdditionalNotes: "Prefers short songs.",
		},
	}

	opts := buildLiveKitWorkspaceHydrationOptions(baseWorkspace, bootstrap, "# Identity\n\nDynamic identity.")

	if opts.SkillsSourceDir != filepath.Join(baseWorkspace, "skills") {
		t.Fatalf("SkillsSourceDir = %q, want base workspace skills", opts.SkillsSourceDir)
	}
	if !strings.Contains(opts.IdentityContent, "Dynamic identity") {
		t.Fatalf("IdentityContent = %q", opts.IdentityContent)
	}
	for _, want := range []string{"Rahul", "Age: 10", "Interests: flowers", "Primary language: Hindi", "Prefers short songs"} {
		if !strings.Contains(opts.UserContent, want) {
			t.Fatalf("UserContent missing %q: %q", want, opts.UserContent)
		}
	}
	for _, want := range []string{"Rahul likes music", "Rahul likes sunflowers", "Cheeko (assistant)"} {
		if !strings.Contains(opts.MemoryContent, want) {
			t.Fatalf("MemoryContent missing %q: %q", want, opts.MemoryContent)
		}
	}
}

func TestBuildLiveKitWorkspaceHydrationOptionsBuildsIdentityFromChildProfileFallback(t *testing.T) {
	baseWorkspace := filepath.Join(t.TempDir(), "workspace")
	bootstrap := roomMetadataBootstrap{
		Source: bootstrapSourceRoomMetadata,
		Metadata: roomMetadata{
			ChildProfile: roomMetadataChildProfile{
				Name:      "Rahul",
				Age:       6,
				Gender:    "male",
				Interests: "science, music",
			},
			PrimaryLanguage: "en",
		},
	}

	opts := buildLiveKitWorkspaceHydrationOptions(baseWorkspace, bootstrap, "")
	for _, want := range []string{
		"Active child profile for this session",
		"Name: Rahul",
		"Age: 6",
		"Gender: male",
		"Interests: science, music",
		"Primary language: en",
	} {
		if !strings.Contains(opts.IdentityContent, want) {
			t.Fatalf("IdentityContent missing %q: %q", want, opts.IdentityContent)
		}
	}
}

func TestHydrateLiveKitWorkspaceSkeletonRepairsBlankCoreFiles(t *testing.T) {
	workspace := t.TempDir()
	blankFiles := []string{
		"AGENT.md",
		"USER.md",
		"SOUL.md",
		"HEARTBEAT.md",
		"memory/MEMORY.md",
	}
	for _, rel := range blankFiles {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(" \n\t"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", rel, err)
		}
	}

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	assertHasContent := func(rel string, contains string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", rel, err)
		}
		if !strings.Contains(string(data), contains) {
			t.Fatalf("%s should contain %q, got %q", rel, contains, string(data))
		}
	}

	assertHasContent("AGENT.md", "No room identity has been hydrated")
	assertHasContent("USER.md", "No user profile override has been hydrated")
	assertHasContent("SOUL.md", "Use the active LiveKit room identity")
	assertHasContent("HEARTBEAT.md", "LiveKit workspace hydrated")
	assertHasContent("memory/MEMORY.md", "No durable memory has been hydrated yet")
}

func TestHydrateLiveKitWorkspaceSkeletonSeedsCoreFilesFromTemplateSources(t *testing.T) {
	workspace := t.TempDir()
	templateWorkspace := t.TempDir()

	mustWriteFile(t, filepath.Join(templateWorkspace, "AGENT.md"), "# Agent\n\nTemplate agent.\n")
	mustWriteFile(t, filepath.Join(templateWorkspace, "SOUL.md"), "# Soul\n\nTemplate soul.\n")
	mustWriteFile(t, filepath.Join(templateWorkspace, "USER.md"), "# User\n\nTemplate user.\n")
	mustWriteFile(t, filepath.Join(templateWorkspace, "memory", "MEMORY.md"), "# Memory\n\nTemplate memory.\n")

	if _, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{templateWorkspace},
	}); err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	assertFileEquals := func(rel, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", rel, err)
		}
		if got := string(data); got != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}

	assertFileEquals("AGENT.md", "# Agent\n\nTemplate agent.\n")
	assertFileEquals("SOUL.md", "# Soul\n\nTemplate soul.\n")
	assertFileEquals("USER.md", "# User\n\nTemplate user.\n")
	assertFileEquals("memory/MEMORY.md", "# Memory\n\nTemplate memory.\n")
}

func TestHydrateLiveKitWorkspaceSkeletonCopiesSkillsFromFallbackSources(t *testing.T) {
	workspace := t.TempDir()
	tmp := t.TempDir()
	baseSkills := filepath.Join(tmp, "base", "skills")
	globalSkills := filepath.Join(tmp, "global-skills")
	builtinSkills := filepath.Join(tmp, "builtin-skills")

	mustWriteFile(t, filepath.Join(globalSkills, "weather", "SKILL.md"), "# Weather\n\nglobal weather")
	mustWriteFile(t, filepath.Join(builtinSkills, "agent-browser", "SKILL.md"), "# Browser\n\nbuiltin browser")
	mustWriteFile(t, filepath.Join(builtinSkills, "weather", "SKILL.md"), "# Weather\n\nbuiltin weather")

	result, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		SkillsSourceDirs: []string{baseSkills, globalSkills, builtinSkills},
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}
	if result.SkillsCopied != 2 {
		t.Fatalf("SkillsCopied = %d, want 2", result.SkillsCopied)
	}

	weather, err := os.ReadFile(filepath.Join(workspace, "skills", "weather", "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile(weather) error = %v", err)
	}
	if !strings.Contains(string(weather), "global weather") {
		t.Fatalf("weather should prefer global over builtin, got %q", string(weather))
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "agent-browser", "SKILL.md")); err != nil {
		t.Fatalf("expected fallback builtin skill: %v", err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLiveKitActiveSkillsReportsMissingSkills(t *testing.T) {
	workspace := t.TempDir()
	mustWriteFile(t, filepath.Join(workspace, "skills", "weather", "SKILL.md"), "---\nname: weather\n---\n# Weather\n")

	installed, missing := validateLiveKitActiveSkills(workspace, []string{"weather", "agent-browser", "weather"})

	if got, want := strings.Join(installed, ","), "weather"; got != want {
		t.Fatalf("installed = %q, want %q", got, want)
	}
	if got, want := strings.Join(missing, ","), "agent-browser"; got != want {
		t.Fatalf("missing = %q, want %q", got, want)
	}
}
