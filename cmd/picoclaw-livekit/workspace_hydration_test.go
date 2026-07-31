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
	assertFilePerm(t, path, 0o600)
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
	for _, want := range []string{"Rahul", "Age: 10", "Interests: flowers", "Primary language: Hindi", "Timezone: Asia/Kolkata", "Prefers short songs"} {
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
		"Timezone: Asia/Kolkata",
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

func TestHydrateLiveKitWorkspaceSkeletonFirstTimeOverwritesTemplateUserWithMetadata(t *testing.T) {
	workspace := t.TempDir()
	templateWorkspace := t.TempDir()

	mustWriteFile(t, filepath.Join(templateWorkspace, "USER.md"), "# User\n\n- Name: Template Kid\n- Age: 8 years old\n")

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs:    []string{templateWorkspace},
		FirstTimeWorkspace:    true,
		UserContent:           "# User\n\n- Name: Rahul\n- Age: 6 years old\n- Interests: science\n",
		IdentityContent:       "# Identity\n\nRoom profile",
		SessionContextContent: "",
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	userData, err := os.ReadFile(filepath.Join(workspace, "USER.md"))
	if err != nil {
		t.Fatalf("ReadFile(USER.md) error = %v", err)
	}
	userText := string(userData)
	if !strings.Contains(userText, "Name: Rahul") {
		t.Fatalf("USER.md should include room metadata name, got %q", userText)
	}
	if strings.Contains(userText, "Template Kid") {
		t.Fatalf("USER.md should not keep template user on first-time workspace, got %q", userText)
	}
}

func TestHydrateLiveKitWorkspaceSkeletonFirstTimeRendersTemplateUserAndKeepsSections(t *testing.T) {
	workspace := t.TempDir()
	templateWorkspace := t.TempDir()

	mustWriteFile(t, filepath.Join(templateWorkspace, "USER.md"), `# User

Information about the user goes here.

## User Information
{{ if .ChildProfile.Name }}- Name: {{ .ChildProfile.Name }}{{ end }}
{{ if .ChildProfile.Age }}- Age: {{ .ChildProfile.Age }} years old{{ end }}
{{ if .ChildProfile.Interests }}- Interests: {{ .ChildProfile.Interests }}{{ end }}

## Preferences
- Communication style: (casual/formal)

## Learning Goals
- What the user wants to learn from AI
`)

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{templateWorkspace},
		FirstTimeWorkspace: true,
		UserContent:        "# User\n\n- Name: Fallback\n",
		ChildProfile: roomMetadataChildProfile{
			Name:      "Shriyansh",
			Age:       4,
			Interests: "space, stories, cricket",
		},
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	userData, err := os.ReadFile(filepath.Join(workspace, "USER.md"))
	if err != nil {
		t.Fatalf("ReadFile(USER.md) error = %v", err)
	}
	userText := string(userData)
	for _, want := range []string{
		"Information about the user goes here.",
		"## Preferences",
		"## Learning Goals",
		"- Name: Shriyansh",
		"- Age: 4 years old",
		"- Interests: space, stories, cricket",
	} {
		if !strings.Contains(userText, want) {
			t.Fatalf("USER.md missing %q: %q", want, userText)
		}
	}
	if strings.Contains(userText, "{{") {
		t.Fatalf("USER.md should not keep template markers: %q", userText)
	}
}

func TestHydrateLiveKitWorkspaceSkeletonRendersMemoryTemplateFromChildProfile(t *testing.T) {
	workspace := t.TempDir()
	templateWorkspace := t.TempDir()

	mustWriteFile(t, filepath.Join(templateWorkspace, "memory", "MEMORY.md"), `# Memory

{% if child_name %}
Child Profile:
- Name: {{ child_name }}
{% if child_age %}- Age: {{ child_age }} years old{% endif %}
{% if child_interests %}- Interests: {{ child_interests }}{% endif %}
{% endif %}
`)

	_, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{templateWorkspace},
		ChildProfile: roomMetadataChildProfile{
			Name:      "Aarav",
			Age:       7,
			Interests: "space, music",
		},
	})
	if err != nil {
		t.Fatalf("hydrateLiveKitWorkspaceSkeleton returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("ReadFile(MEMORY.md) error = %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"Child Profile:",
		"Name: Aarav",
		"Age: 7 years old",
		"Interests: space, music",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MEMORY.md missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "{%") {
		t.Fatalf("MEMORY.md still contains template markers: %q", got)
	}
}

func TestRenderMemoryTemplateWithChildProfile(t *testing.T) {
	templateText := `# Memory

{% if child_name %}
Child Profile:
- Name: {{ child_name }}
{% if child_age %}- Age: {{ child_age }} years old{% endif %}
{% if child_interests %}- Interests: {{ child_interests }}{% endif %}
{% endif %}
`
	rendered, ok := renderMemoryTemplateWithChildProfile(templateText, roomMetadataChildProfile{
		Name:      "Aarav",
		Age:       7,
		Interests: "space, music",
	})
	if !ok {
		t.Fatal("renderMemoryTemplateWithChildProfile = false, want true")
	}
	for _, want := range []string{"Child Profile:", "Name: Aarav", "Age: 7 years old", "Interests: space, music"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered memory missing %q: %q", want, rendered)
		}
	}
}

func TestHydrateLiveKitWorkspaceSkeletonDoesNotOverrideRenderedMemoryOnSecondRun(t *testing.T) {
	workspace := t.TempDir()
	templateWorkspace := t.TempDir()

	mustWriteFile(t, filepath.Join(templateWorkspace, "memory", "MEMORY.md"), `# Memory

Child Profile:
- Name: {{ child_name }}
`)

	if _, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{templateWorkspace},
		ChildProfile: roomMetadataChildProfile{
			Name: "Aarav",
		},
	}); err != nil {
		t.Fatalf("first hydrate returned error: %v", err)
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	firstContent, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("ReadFile(first MEMORY.md) error = %v", err)
	}

	if _, err := hydrateLiveKitWorkspaceSkeleton(workspace, liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{templateWorkspace},
		ChildProfile: roomMetadataChildProfile{
			Name: "Ira",
		},
	}); err != nil {
		t.Fatalf("second hydrate returned error: %v", err)
	}

	secondContent, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("ReadFile(second MEMORY.md) error = %v", err)
	}
	if string(secondContent) != string(firstContent) {
		t.Fatalf("MEMORY.md should not be overwritten on second run: first=%q second=%q", string(firstContent), string(secondContent))
	}
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

func TestUserProfileHasChildDetails(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name: "rich profile has name",
			content: `# User

- Name: Rahul
- Primary language: en
`,
			want: true,
		},
		{
			name: "language only is sparse",
			content: `# User

- Primary language: en
`,
			want: false,
		},
		{
			name: "additional notes counts as details",
			content: `# User

## Additional Notes

Prefers short stories.
`,
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userProfileHasChildDetails(tc.content); got != tc.want {
				t.Fatalf("userProfileHasChildDetails() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRefreshUserFromMetadata(t *testing.T) {
	tmp := t.TempDir()
	userPath := filepath.Join(tmp, "USER.md")

	if should, reason := shouldRefreshUserFromMetadata(userPath, true); !should || reason != "first_time_workspace" {
		t.Fatalf("first time = (%v, %q), want (true, first_time_workspace)", should, reason)
	}

	if should, reason := shouldRefreshUserFromMetadata(userPath, false); !should || reason != "missing_user_md" {
		t.Fatalf("missing file = (%v, %q), want (true, missing_user_md)", should, reason)
	}

	if err := os.WriteFile(userPath, []byte("# User\n\n- Primary language: en\n"), 0o644); err != nil {
		t.Fatalf("WriteFile sparse USER.md error = %v", err)
	}
	if should, reason := shouldRefreshUserFromMetadata(userPath, false); !should || reason != "missing_child_profile_fields" {
		t.Fatalf("sparse file = (%v, %q), want (true, missing_child_profile_fields)", should, reason)
	}
	if should, reason := shouldRefreshUserFromMetadata(userPath, true); !should || reason != "first_time_workspace_existing_user_md" {
		t.Fatalf("first time existing file = (%v, %q), want (true, first_time_workspace_existing_user_md)", should, reason)
	}

	if err := os.WriteFile(userPath, []byte("# User\n\n- Name: Rahul\n- Primary language: en\n"), 0o644); err != nil {
		t.Fatalf("WriteFile rich-but-no-timezone USER.md error = %v", err)
	}
	if should, reason := shouldRefreshUserFromMetadata(userPath, false); should || reason != "existing_user_profile" {
		t.Fatalf("rich but no timezone = (%v, %q), want (false, existing_user_profile)", should, reason)
	}

	if err := os.WriteFile(userPath, []byte("# User\n\n- Name: Rahul\n- Primary language: en\n- Timezone: Asia/Kolkata\n"), 0o644); err != nil {
		t.Fatalf("WriteFile rich USER.md error = %v", err)
	}
	if should, reason := shouldRefreshUserFromMetadata(userPath, false); should || reason != "existing_user_profile" {
		t.Fatalf("rich file = (%v, %q), want (false, existing_user_profile)", should, reason)
	}
}

func TestUpsertUserMarkdownField(t *testing.T) {
	content := "# User\n\n- Name: Rahul\n- Primary language: en\n\n## Additional Notes\n\nhello\n"
	updated, changed := upsertUserMarkdownField(content, "Timezone", "Asia/Kolkata")
	if !changed {
		t.Fatalf("missing field => changed=false")
	}
	if !strings.Contains(updated, "- Primary language: en\n- Timezone: Asia/Kolkata\n\n## Additional Notes") {
		t.Fatalf("expected field appended to the list above the notes, got %q", updated)
	}

	updated, changed = upsertUserMarkdownField("# User\n\n- Name: Rahul\n- Timezone: America/New_York\n", "Timezone", "Asia/Kolkata")
	if !changed || !strings.Contains(updated, "- Timezone: Asia/Kolkata") {
		t.Fatalf("changed field => changed=%v content=%q", changed, updated)
	}
	if strings.Contains(updated, "America/New_York") {
		t.Fatalf("expected old value replaced, got %q", updated)
	}

	unchanged := "# User\n\n- Name: Rahul\n- Timezone: Asia/Kolkata\n"
	if updated, changed = upsertUserMarkdownField(unchanged, "Timezone", "Asia/Kolkata"); changed || updated != unchanged {
		t.Fatalf("unchanged field => changed=%v content=%q", changed, updated)
	}
	if updated, changed = upsertUserMarkdownField(unchanged, "Gender", "   "); changed || updated != unchanged {
		t.Fatalf("empty value must not touch the file => changed=%v content=%q", changed, updated)
	}
}

func TestSyncUserProfileFromMetadata(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "USER.md")
	// USER.md as restored from the Manager DB: stale age, plus a fact the agent wrote itself.
	existing := "# User\n\n## User Information\n\n- Name: Rahul\n- Age: 6 years old\n- Timezone: Asia/Kolkata\n\n## Additional Notes\n\nAfraid of the dark.\n"
	if err := os.WriteFile(userPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile USER.md error = %v", err)
	}

	md := roomMetadata{}
	md.ChildProfile.Name = "Rahul"
	md.ChildProfile.Age = 7
	md.ChildProfile.Interests = "dinosaurs, space"
	md.ChildProfile.Timezone = "Asia/Kolkata"

	changed, err := syncUserProfileFromMetadata(userPath, md)
	if err != nil {
		t.Fatalf("syncUserProfileFromMetadata error = %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v, want Interests and Age only", changed)
	}

	data, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile USER.md error = %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"- Age: 7 years old",
		"- Interests: dinosaurs, space",
		"Afraid of the dark.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("USER.md missing %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "6 years old") {
		t.Fatalf("stale age survived, got %q", got)
	}

	// Second run is a no-op once the file already matches the metadata.
	changed, err = syncUserProfileFromMetadata(userPath, md)
	if err != nil {
		t.Fatalf("second syncUserProfileFromMetadata error = %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("second run changed = %v, want none", changed)
	}
}

func TestSyncUserProfileFromMetadataCases(t *testing.T) {
	profile := func(name string, age int, gender, interests, timezone string) roomMetadata {
		md := roomMetadata{}
		md.ChildProfile.Name = name
		md.ChildProfile.Age = age
		md.ChildProfile.Gender = gender
		md.ChildProfile.Interests = interests
		md.ChildProfile.Timezone = timezone
		return md
	}

	cases := []struct {
		name        string
		existing    string
		metadata    roomMetadata
		wantChanged []string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "rename and dob change rewrite name and age",
			existing:    "# User\n\n- Name: Rahul\n- Age: 6 years old\n- Timezone: Asia/Kolkata\n",
			metadata:    profile("Rahul Krishna", 7, "", "", "Asia/Kolkata"),
			wantChanged: []string{"Name", "Age"},
			wantContain: []string{"- Name: Rahul Krishna", "- Age: 7 years old"},
			wantAbsent:  []string{"- Name: Rahul\n", "6 years old"},
		},
		{
			name:        "field absent on disk is inserted once",
			existing:    "# User\n\n- Name: Rahul\n- Timezone: Asia/Kolkata\n",
			metadata:    profile("Rahul", 0, "", "dinosaurs", "Asia/Kolkata"),
			wantChanged: []string{"Interests"},
			wantContain: []string{"- Interests: dinosaurs"},
		},
		{
			name:        "cleared portal field does not erase the disk line",
			existing:    "# User\n\n- Name: Rahul\n- Gender: male\n- Timezone: Asia/Kolkata\n",
			metadata:    profile("Rahul", 0, "", "", "Asia/Kolkata"),
			wantChanged: nil,
			wantContain: []string{"- Gender: male"},
		},
		{
			name:        "blank metadata timezone falls back to the default",
			existing:    "# User\n\n- Name: Rahul\n",
			metadata:    profile("Rahul", 0, "", "", ""),
			wantChanged: []string{"Timezone"},
			wantContain: []string{"- Name: Rahul\n- Timezone: Asia/Kolkata"},
		},
		{
			name:        "capitalization fix is a real edit",
			existing:    "# User\n\n- Name: rahul\n- Timezone: Asia/Kolkata\n",
			metadata:    profile("Rahul", 0, "", "", "Asia/Kolkata"),
			wantChanged: []string{"Name"},
			wantContain: []string{"- Name: Rahul"},
			wantAbsent:  []string{"- Name: rahul"},
		},
		{
			name:        "lowercase label on disk is updated in place not duplicated",
			existing:    "# User\n\n- name: Rahul\n- Timezone: Asia/Kolkata\n",
			metadata:    profile("Rahul Krishna", 0, "", "", "Asia/Kolkata"),
			wantChanged: []string{"Name"},
			wantContain: []string{"- Name: Rahul Krishna"},
			wantAbsent:  []string{"- name: Rahul"},
		},
		{
			name:        "agent written sections survive the merge",
			existing:    "# User\n\n## User Information\n\n- Name: Rahul\n- Age: 6 years old\n- Timezone: Asia/Kolkata\n\n## Additional Notes\n\nAfraid of the dark.\nLoves the red truck.\n",
			metadata:    profile("Rahul", 7, "", "space", "Asia/Kolkata"),
			wantChanged: []string{"Interests", "Age"},
			wantContain: []string{
				"## Additional Notes",
				"Afraid of the dark.",
				"Loves the red truck.",
				"- Age: 7 years old\n- Timezone: Asia/Kolkata\n- Interests: space\n\n## Additional Notes",
			},
		},
		{
			name:        "crlf file stays parseable",
			existing:    "# User\r\n\r\n- Name: Rahul\r\n- Age: 6 years old\r\n- Timezone: Asia/Kolkata\r\n",
			metadata:    profile("Rahul", 7, "", "", "Asia/Kolkata"),
			wantChanged: []string{"Age"},
			wantContain: []string{"- Age: 7 years old"},
			wantAbsent:  []string{"6 years old"},
		},
		{
			name:        "placeholder user md gets the profile appended",
			existing:    "# User\n\nNo user profile override has been hydrated for this session.\n",
			metadata:    profile("Rahul", 7, "male", "", "Asia/Kolkata"),
			wantChanged: []string{"Name", "Gender", "Age", "Timezone"},
			wantContain: []string{"- Name: Rahul", "- Gender: male", "- Age: 7 years old", "- Timezone: Asia/Kolkata"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userPath := filepath.Join(t.TempDir(), "USER.md")
			if err := os.WriteFile(userPath, []byte(tc.existing), 0o644); err != nil {
				t.Fatalf("WriteFile USER.md error = %v", err)
			}

			changed, err := syncUserProfileFromMetadata(userPath, tc.metadata)
			if err != nil {
				t.Fatalf("syncUserProfileFromMetadata error = %v", err)
			}
			if strings.Join(changed, ",") != strings.Join(tc.wantChanged, ",") {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChanged)
			}

			data, err := os.ReadFile(userPath)
			if err != nil {
				t.Fatalf("ReadFile USER.md error = %v", err)
			}
			got := string(data)
			for _, want := range tc.wantContain {
				if !strings.Contains(got, want) {
					t.Fatalf("USER.md missing %q, got:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.wantAbsent {
				if strings.Contains(got, unwanted) {
					t.Fatalf("USER.md still contains %q, got:\n%s", unwanted, got)
				}
			}
			if n := strings.Count(got, "- Name:"); n > 1 {
				t.Fatalf("duplicated Name line (%d), got:\n%s", n, got)
			}

			// Every case must converge: a second run with the same metadata changes nothing.
			second, err := syncUserProfileFromMetadata(userPath, tc.metadata)
			if err != nil {
				t.Fatalf("second syncUserProfileFromMetadata error = %v", err)
			}
			if len(second) != 0 {
				t.Fatalf("second run changed = %v, want none", second)
			}
		})
	}
}

func TestSyncUserProfileFromMetadataMissingFileIsNoop(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "USER.md")
	md := roomMetadata{}
	md.ChildProfile.Name = "Rahul"

	changed, err := syncUserProfileFromMetadata(userPath, md)
	if err != nil {
		t.Fatalf("syncUserProfileFromMetadata error = %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v, want none", changed)
	}
	// Hydration owns first-time creation; the merge must not create the file itself.
	if _, err := os.Stat(userPath); !os.IsNotExist(err) {
		t.Fatalf("USER.md was created by the merge, stat err = %v", err)
	}
}
