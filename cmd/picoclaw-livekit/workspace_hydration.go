package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/config"
)

type liveKitWorkspaceHydrationOptions struct {
	IdentityContent       string
	UserContent           string
	MemoryContent         string
	ChildProfile          roomMetadataChildProfile
	FirstTimeWorkspace    bool
	SessionContextContent string
	TemplateSourceDir     string
	TemplateSourceDirs    []string
	SkillsSourceDir       string
	SkillsSourceDirs      []string

	// Phase 3 (ADR-0003): persona pulled per session by characterId.
	PersonaSystemPrompt string // injected into the AGENT.md scaffold's <!-- PERSONA --> slot
	SoulContent         string // written verbatim to SOUL.md
	RegeneratePersona   bool   // true when the Manager pull succeeded -> overwrite AGENT.md/SOUL.md every session
	SessionLanguage     string // fills the scaffold's <!-- LANGUAGE --> slot (card/character language)
}

const personaPlaceholder = "<!-- PERSONA -->"
const languagePlaceholder = "<!-- LANGUAGE -->"

// injectLanguage fills the scaffold's <!-- LANGUAGE --> slot with the session language
// (default English) so the LLM responds in the card/character language.
func injectLanguage(content, language string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		language = "English"
	}
	return strings.ReplaceAll(content, languagePlaceholder, language)
}

// injectPersona fills the scaffold's persona slot. Empty persona strips the slot;
// a scaffold without the slot gets the persona prepended (legacy templates).
func injectPersona(scaffold, persona string) string {
	persona = strings.TrimSpace(persona)
	if strings.Contains(scaffold, personaPlaceholder) {
		return strings.ReplaceAll(scaffold, personaPlaceholder, persona)
	}
	if persona == "" {
		return scaffold
	}
	return persona + "\n\n" + scaffold
}

// readFirstWorkspaceTemplateFile returns the first non-blank copy of rel found in sourceDirs.
func readFirstWorkspaceTemplateFile(sourceDirs []string, rel string) string {
	for _, dir := range sourceDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
			if strings.TrimSpace(string(data)) != "" {
				return string(data)
			}
		}
	}
	return ""
}

type liveKitWorkspaceHydrationResult struct {
	MemoryWritten bool
	SkillsCopied  int
}

type workspaceTemplateFileSpec struct {
	Perm os.FileMode
}

var workspaceTemplateFiles = map[string]workspaceTemplateFileSpec{
	"AGENT.md":         {Perm: 0o644},
	"SOUL.md":          {Perm: 0o644},
	"USER.md":          {Perm: 0o644},
	"memory/MEMORY.md": {Perm: 0o600},
}

var (
	jinjaIfPattern    = regexp.MustCompile(`\{%\s*if\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*%\}`)
	jinjaEndIfPattern = regexp.MustCompile(`\{%\s*endif\s*%\}`)
	jinjaVarPattern   = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)
)

func buildLiveKitWorkspaceHydrationOptions(
	baseWorkspace string,
	bootstrap roomMetadataBootstrap,
	identityContent string,
) liveKitWorkspaceHydrationOptions {
	opts := liveKitWorkspaceHydrationOptions{
		IdentityContent: strings.TrimSpace(identityContent),
	}
	if strings.TrimSpace(baseWorkspace) != "" {
		opts.TemplateSourceDir = baseWorkspace
		opts.TemplateSourceDirs = liveKitWorkspaceTemplateDirs(baseWorkspace)
		opts.SkillsSourceDir = filepath.Join(baseWorkspace, "skills")
		opts.SkillsSourceDirs = liveKitSkillSourceDirs(baseWorkspace)
	}

	md := bootstrap.Metadata
	if opts.IdentityContent == "" {
		opts.IdentityContent = formatRoomMetadataIdentityContent(md)
	}
	opts.UserContent = formatRoomMetadataUserContent(md)
	opts.MemoryContent = formatRoomMetadataMemoryContent(md)
	opts.ChildProfile = md.ChildProfile
	return opts
}

func hydrateLiveKitWorkspaceSkeleton(workspace string, opts liveKitWorkspaceHydrationOptions) (liveKitWorkspaceHydrationResult, error) {
	var result liveKitWorkspaceHydrationResult
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return result, fmt.Errorf("workspace is empty")
	}

	for _, rel := range []string{
		"memory",
		"sessions",
		"skills",
		"cron",
		"state",
	} {
		if err := os.MkdirAll(filepath.Join(workspace, rel), 0o755); err != nil {
			return result, err
		}
	}

	templateSources := opts.TemplateSourceDirs
	if len(templateSources) == 0 && strings.TrimSpace(opts.TemplateSourceDir) != "" {
		templateSources = []string{opts.TemplateSourceDir}
	}
	if err := seedWorkspaceCoreFilesFromSources(workspace, templateSources); err != nil {
		return result, err
	}

	// AGENT.md = persona-agnostic scaffold + Manager systemPrompt (ADR-0003).
	// When the persona resolved, regenerate every session; otherwise keep the last-rendered
	// AGENT.md (degraded fallback) and only write a fresh scaffold if it's missing/blank.
	agentPath := filepath.Join(workspace, "AGENT.md")
	scaffold := readFirstWorkspaceTemplateFile(templateSources, "AGENT.md")
	var agentContent string
	persona := strings.TrimSpace(opts.PersonaSystemPrompt)
	if strings.Contains(persona, languagePlaceholder) {
		// The DB system_prompt holds a FULL AGENT.md (scaffold + persona baked together,
		// discriminated by the presence of the <!-- LANGUAGE --> slot). Use it verbatim;
		// do not read or merge the on-disk scaffold and do not injectPersona.
		agentContent = persona
	} else if strings.TrimSpace(scaffold) != "" {
		agentContent = injectPersona(scaffold, persona)
	} else {
		agentContent = strings.TrimSpace(opts.IdentityContent) // legacy fallback
	}
	if strings.TrimSpace(agentContent) == "" {
		agentContent = "# LiveKit Voice Agent\n\nNo room identity has been hydrated for this session.\n"
	}
	agentContent = injectLanguage(agentContent, opts.SessionLanguage)
	if opts.RegeneratePersona && strings.TrimSpace(opts.PersonaSystemPrompt) != "" {
		if err := writeFileWithMode(agentPath, []byte(ensureTrailingNewline(agentContent)), 0o644); err != nil {
			return result, err
		}
	} else {
		// Degraded/no-persona: write the (placeholder-stripped) scaffold only if AGENT.md is
		// missing/blank or still holds the raw <!-- PERSONA --> slot from seeding; otherwise
		// keep the last-rendered persona (never clobber a good render when the Manager is down).
		existing, readErr := os.ReadFile(agentPath)
		if os.IsNotExist(readErr) || strings.TrimSpace(string(existing)) == "" || strings.Contains(string(existing), personaPlaceholder) {
			if err := writeFileWithMode(agentPath, []byte(ensureTrailingNewline(agentContent)), 0o644); err != nil {
				return result, err
			}
		}
	}

	userPath := filepath.Join(workspace, "USER.md")
	userContent := strings.TrimSpace(opts.UserContent)
	if userContent != "" {
		if shouldRefresh, _ := shouldRefreshUserFromMetadata(userPath, opts.FirstTimeWorkspace); shouldRefresh {
			contentToWrite := ensureTrailingNewline(userContent)
			if existingUser, err := os.ReadFile(userPath); err == nil && hasTemplateMarkers(string(existingUser)) {
				if rendered, ok := renderUserTemplateWithChildProfile(string(existingUser), opts.ChildProfile); ok {
					contentToWrite = ensureTrailingNewline(rendered)
				}
			}
			if err := os.WriteFile(userPath, []byte(contentToWrite), 0o644); err != nil {
				return result, err
			}
		} else {
			if err := writeFileIfMissingOrBlank(userPath, ensureTrailingNewline(userContent), 0o644); err != nil {
				return result, err
			}
		}
	} else {
		if err := writeFileIfMissingOrBlank(userPath, "# User\n\nNo user profile override has been hydrated for this session.\n", 0o644); err != nil {
			return result, err
		}
	}
	// SOUL.md = Manager soul, regenerated every session when the persona resolved;
	// otherwise keep the last-rendered SOUL.md (degraded) or seed the scaffold soul once.
	soulPath := filepath.Join(workspace, "SOUL.md")
	if opts.RegeneratePersona && strings.TrimSpace(opts.SoulContent) != "" {
		if err := writeFileWithMode(soulPath, []byte(ensureTrailingNewline(strings.TrimSpace(opts.SoulContent))), 0o644); err != nil {
			return result, err
		}
	} else {
		soulTemplate := strings.TrimSpace(readFirstWorkspaceTemplateFile(templateSources, "SOUL.md"))
		if soulTemplate == "" {
			soulTemplate = "# Soul\n\nUse the active LiveKit room identity and child context for this voice session."
		}
		if err := writeFileIfMissingOrBlank(soulPath, ensureTrailingNewline(soulTemplate), 0o644); err != nil {
			return result, err
		}
	}
	if err := writeFileIfMissingOrBlank(filepath.Join(workspace, "HEARTBEAT.md"), "# Heartbeat\n\nLiveKit workspace hydrated.\n", 0o644); err != nil {
		return result, err
	}
	if err := writeFileIfMissing(filepath.Join(workspace, "heartbeat.log"), "", 0o644); err != nil {
		return result, err
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	existingMemory, memoryReadErr := os.ReadFile(memoryPath)
	existingMemoryContent := ""
	if memoryReadErr == nil {
		existingMemoryContent = string(existingMemory)
	} else if !os.IsNotExist(memoryReadErr) {
		return result, memoryReadErr
	}

	if shouldInitializeMemoryContent(existingMemoryContent, memoryReadErr) {
		contentToWrite := ""
		if hasTemplateMarkers(existingMemoryContent) {
			if rendered, ok := renderMemoryTemplateWithChildProfile(existingMemoryContent, opts.ChildProfile); ok {
				contentToWrite = rendered
			}
		}
		if strings.TrimSpace(contentToWrite) == "" {
			memoryContent := strings.TrimSpace(opts.MemoryContent)
			if memoryContent != "" {
				if !strings.HasPrefix(memoryContent, "#") {
					memoryContent = "# Memory\n\n" + memoryContent
				}
				contentToWrite = memoryContent
			}
		}
		if strings.TrimSpace(contentToWrite) == "" {
			contentToWrite = "# Memory\n\nNo durable memory has been hydrated yet.\n"
		}
		if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
			return result, err
		}
		if err := writeFileWithMode(memoryPath, []byte(ensureTrailingNewline(contentToWrite)), 0o600); err != nil {
			return result, err
		}
		result.MemoryWritten = true
	}

	sessionContextContent := strings.TrimSpace(opts.SessionContextContent)
	if sessionContextContent != "" {
		if err := writeFileWithMode(
			filepath.Join(workspace, "sessions", "manager_recent_voice_context.md"),
			[]byte(ensureTrailingNewline(sessionContextContent)),
			0o600,
		); err != nil {
			return result, err
		}
	}

	skillSources := opts.SkillsSourceDirs
	if len(skillSources) == 0 && strings.TrimSpace(opts.SkillsSourceDir) != "" {
		skillSources = []string{opts.SkillsSourceDir}
	}
	copied, err := copyWorkspaceSkillsFromSources(skillSources, filepath.Join(workspace, "skills"))
	if err != nil {
		return result, err
	}
	result.SkillsCopied = copied

	return result, nil
}

func liveKitSkillSourceDirs(baseWorkspace string) []string {
	sources := []string{}
	if strings.TrimSpace(baseWorkspace) != "" {
		sources = append(sources, filepath.Join(baseWorkspace, "skills"))
	}

	if exeDir := liveKitExecutableDir(); exeDir != "" {
		sources = append(sources, filepath.Join(exeDir, "workspace", "skills"))
		sources = append(sources, filepath.Join(exeDir, "workspace-template", "skills"))
	}

	globalConfigDir := getLiveKitGlobalConfigDir()
	if globalConfigDir != "" {
		sources = append(sources, filepath.Join(globalConfigDir, "skills"))
		sources = append(sources, filepath.Join(globalConfigDir, "picoclaw", "skills"))
	}

	if builtin := strings.TrimSpace(os.Getenv(config.EnvBuiltinSkills)); builtin != "" {
		sources = append(sources, builtin)
	} else if wd, err := os.Getwd(); err == nil {
		sources = append(sources, filepath.Join(wd, "workspace", "skills"))
		sources = append(sources, filepath.Join(wd, "skills"))
	}

	return cleanUniquePaths(sources)
}

func liveKitWorkspaceTemplateDirs(baseWorkspace string) []string {
	sources := []string{}
	if strings.TrimSpace(baseWorkspace) != "" {
		sources = append(sources, baseWorkspace)
	}

	if exeDir := liveKitExecutableDir(); exeDir != "" {
		sources = append(sources, filepath.Join(exeDir, "workspace"))
		sources = append(sources, filepath.Join(exeDir, "workspace-template"))
	}

	globalConfigDir := getLiveKitGlobalConfigDir()
	if globalConfigDir != "" {
		sources = append(sources, filepath.Join(globalConfigDir, "workspace"))
	}

	if wd, err := os.Getwd(); err == nil {
		sources = append(sources, filepath.Join(wd, "workspace"))
		sources = append(sources, filepath.Join(wd, "cmd", "picoclaw", "internal", "onboard", "workspace"))
	}

	return cleanUniquePaths(sources)
}

func liveKitExecutableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(filepath.Dir(exe))
}

func getLiveKitGlobalConfigDir() string {
	if home := strings.TrimSpace(os.Getenv(config.EnvHome)); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, pkg.DefaultPicoClawHome)
}

func cleanUniquePaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func seedWorkspaceCoreFilesFromSources(workspace string, sourceDirs []string) error {
	if strings.TrimSpace(workspace) == "" || len(sourceDirs) == 0 {
		return nil
	}

	for rel, spec := range workspaceTemplateFiles {
		target := filepath.Join(workspace, filepath.FromSlash(rel))
		targetData, err := os.ReadFile(target)
		if err == nil && strings.TrimSpace(string(targetData)) != "" {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		for _, sourceDir := range sourceDirs {
			sourceDir = strings.TrimSpace(sourceDir)
			if sourceDir == "" {
				continue
			}
			source := filepath.Join(sourceDir, filepath.FromSlash(rel))
			data, err := os.ReadFile(source)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if strings.TrimSpace(string(data)) == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFileWithMode(target, []byte(ensureTrailingNewline(string(data))), spec.Perm); err != nil {
				return err
			}
			break
		}
	}

	return nil
}

func writeFileIfMissing(path, content string, perm os.FileMode) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileWithMode(path, []byte(content), perm)
}

func writeFileIfMissingOrBlank(path, content string, perm os.FileMode) error {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if strings.TrimSpace(string(data)) != "" {
			return nil
		}
	case os.IsNotExist(err):
		// continue and create
	default:
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileWithMode(path, []byte(content), perm)
}

func writeFileWithMode(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func shouldInitializeMemoryContent(content string, readErr error) bool {
	if os.IsNotExist(readErr) {
		return true
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	if strings.Contains(trimmed, "No durable memory has been hydrated yet.") {
		return true
	}
	return hasTemplateMarkers(trimmed)
}

func hasTemplateMarkers(content string) bool {
	return strings.Contains(content, "{{") || strings.Contains(content, "{%")
}

func normalizeMemoryTemplateSyntax(content string) string {
	content = jinjaIfPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := jinjaIfPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return "{{ if " + memoryTemplateField(parts[1]) + " }}"
	})
	content = jinjaEndIfPattern.ReplaceAllString(content, "{{ end }}")
	content = jinjaVarPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := jinjaVarPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return "{{ " + memoryTemplateField(parts[1]) + " }}"
	})
	return content
}

func memoryTemplateField(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "end", "else", "if", "range", "with":
		return strings.TrimSpace(name)
	case "child_name":
		return ".ChildName"
	case "child_age":
		return ".ChildAge"
	case "child_gender":
		return ".ChildGender"
	case "child_interests":
		return ".ChildInterests"
	case "child_timezone":
		return ".ChildTimezone"
	default:
		return "." + strings.TrimSpace(name)
	}
}

func renderMemoryTemplateWithChildProfile(
	content string,
	child roomMetadataChildProfile,
) (string, bool) {
	if strings.TrimSpace(content) == "" || !hasTemplateMarkers(content) {
		return "", false
	}

	tmplText := normalizeMemoryTemplateSyntax(content)
	tmpl, err := template.New("memory").Option("missingkey=zero").Parse(tmplText)
	if err != nil {
		return "", false
	}

	ctx := struct {
		ChildName      string
		ChildAge       int
		ChildGender    string
		ChildInterests string
		ChildTimezone  string
		ChildProfile   roomMetadataChildProfile
	}{
		ChildName:      strings.TrimSpace(child.Name),
		ChildAge:       child.Age,
		ChildGender:    strings.TrimSpace(child.Gender),
		ChildInterests: strings.TrimSpace(child.Interests),
		ChildTimezone:  strings.TrimSpace(child.Timezone),
		ChildProfile:   child,
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, ctx); err != nil {
		return "", false
	}

	rendered := strings.TrimSpace(out.String())
	if rendered == "" {
		return "", false
	}
	return rendered, true
}

func renderUserTemplateWithChildProfile(
	content string,
	child roomMetadataChildProfile,
) (string, bool) {
	if strings.TrimSpace(content) == "" || !hasTemplateMarkers(content) {
		return "", false
	}

	tmplText := normalizeMemoryTemplateSyntax(content)
	tmpl, err := template.New("user").Option("missingkey=zero").Parse(tmplText)
	if err != nil {
		return "", false
	}

	ctx := struct {
		ChildName      string
		ChildAge       int
		ChildGender    string
		ChildInterests string
		ChildTimezone  string
		ChildProfile   roomMetadataChildProfile
	}{
		ChildName:      strings.TrimSpace(child.Name),
		ChildAge:       child.Age,
		ChildGender:    strings.TrimSpace(child.Gender),
		ChildInterests: strings.TrimSpace(child.Interests),
		ChildTimezone:  strings.TrimSpace(child.Timezone),
		ChildProfile:   child,
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, ctx); err != nil {
		return "", false
	}
	rendered := strings.TrimSpace(out.String())
	if rendered == "" {
		return "", false
	}
	return rendered, true
}

func ensureTrailingNewline(content string) string {
	if strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}

func copyWorkspaceSkills(sourceDir, destinationDir string) (int, error) {
	sourceDir = strings.TrimSpace(sourceDir)
	destinationDir = strings.TrimSpace(destinationDir)
	if sourceDir == "" || destinationDir == "" {
		return 0, nil
	}

	sourceAbs, err := filepath.Abs(sourceDir)
	if err != nil {
		return 0, err
	}
	destinationAbs, err := filepath.Abs(destinationDir)
	if err != nil {
		return 0, err
	}
	if samePath(sourceAbs, destinationAbs) {
		return 0, nil
	}
	if info, err := os.Stat(sourceAbs); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	} else if !info.IsDir() {
		return 0, nil
	}
	if err := os.MkdirAll(destinationAbs, 0o755); err != nil {
		return 0, err
	}

	copiedSkillDirs := map[string]struct{}{}
	err = filepath.WalkDir(sourceAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceAbs, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(destinationAbs, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		if strings.EqualFold(entry.Name(), "SKILL.md") {
			parts := strings.Split(filepath.Clean(rel), string(os.PathSeparator))
			if len(parts) > 1 {
				copiedSkillDirs[parts[0]] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(copiedSkillDirs), nil
}

func copyWorkspaceSkillsFromSources(sourceDirs []string, destinationDir string) (int, error) {
	if len(sourceDirs) == 0 || strings.TrimSpace(destinationDir) == "" {
		return 0, nil
	}

	for i := len(sourceDirs) - 1; i >= 0; i-- {
		if _, err := copyWorkspaceSkills(sourceDirs[i], destinationDir); err != nil {
			return 0, err
		}
	}
	return countWorkspaceSkills(destinationDir)
}

func countWorkspaceSkills(skillsDir string) (int, error) {
	skillsDir = strings.TrimSpace(skillsDir)
	if skillsDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(skillsDir, entry.Name(), "SKILL.md")); err == nil {
			count++
		}
	}
	return count, nil
}

func copyFile(source, destination string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func samePath(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	return err == nil && rel == "."
}

func formatRoomMetadataIdentityContent(md roomMetadata) string {
	name := strings.TrimSpace(md.ChildProfile.Name)
	if name == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# LiveKit Voice Agent\n\n")
	sb.WriteString("Active child profile for this session:\n\n")
	sb.WriteString("- Name: ")
	sb.WriteString(name)
	sb.WriteString("\n")
	if md.ChildProfile.Age > 0 {
		sb.WriteString("- Age: ")
		sb.WriteString(strconv.Itoa(md.ChildProfile.Age))
		sb.WriteString("\n")
	}
	if gender := strings.TrimSpace(md.ChildProfile.Gender); gender != "" {
		sb.WriteString("- Gender: ")
		sb.WriteString(gender)
		sb.WriteString("\n")
	}
	if interests := strings.TrimSpace(md.ChildProfile.Interests); interests != "" {
		sb.WriteString("- Interests: ")
		sb.WriteString(interests)
		sb.WriteString("\n")
	}
	if language := strings.TrimSpace(md.PrimaryLanguage); language != "" {
		sb.WriteString("- Primary language: ")
		sb.WriteString(language)
		sb.WriteString("\n")
	}
	timezone := strings.TrimSpace(md.ChildProfile.Timezone)
	if timezone == "" {
		timezone = "Asia/Kolkata"
	}
	sb.WriteString("- Timezone: ")
	sb.WriteString(timezone)
	sb.WriteString("\n")
	return strings.TrimSpace(sb.String())
}

func formatRoomMetadataUserContent(md roomMetadata) string {
	var sb strings.Builder
	sb.WriteString("# User\n\n")
	sb.WriteString("## User Information\n\n")
	if name := strings.TrimSpace(md.ChildProfile.Name); name != "" {
		sb.WriteString("- Name: ")
		sb.WriteString(name)
		sb.WriteString("\n")
	}
	if md.ChildProfile.Age > 0 {
		sb.WriteString("- Age: ")
		sb.WriteString(strconv.Itoa(md.ChildProfile.Age))
		sb.WriteString(" years old")
		sb.WriteString("\n")
	}
	if interests := strings.TrimSpace(md.ChildProfile.Interests); interests != "" {
		sb.WriteString("- Interests: ")
		sb.WriteString(interests)
		sb.WriteString("\n")
	}
	if gender := strings.TrimSpace(md.ChildProfile.Gender); gender != "" {
		sb.WriteString("- Gender: ")
		sb.WriteString(gender)
		sb.WriteString("\n")
	}
	timezone := strings.TrimSpace(md.ChildProfile.Timezone)
	if timezone == "" {
		timezone = "Asia/Kolkata"
	}
	sb.WriteString("- Timezone: ")
	sb.WriteString(timezone)
	sb.WriteString("\n")
	if language := strings.TrimSpace(md.PrimaryLanguage); language != "" {
		sb.WriteString("- Primary language: ")
		sb.WriteString(language)
		sb.WriteString("\n")
	}
	if notes := strings.TrimSpace(md.AdditionalNotes); notes != "" {
		sb.WriteString("\n## Additional Notes\n\n")
		sb.WriteString(notes)
		sb.WriteString("\n")
	}

	content := strings.TrimSpace(sb.String())
	if content == "# User" {
		return ""
	}
	return content
}

func userProfileHasChildDetails(content string) bool {
	content = strings.ToLower(content)
	for _, marker := range []string{
		"- name:",
		"- age:",
		"- gender:",
		"- interests:",
		"## additional notes",
	} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func shouldRefreshUserFromMetadata(userPath string, firstTimeWorkspace bool) (bool, string) {
	data, err := os.ReadFile(userPath)
	if err != nil {
		if os.IsNotExist(err) {
			if firstTimeWorkspace {
				return true, "first_time_workspace"
			}
			return true, "missing_user_md"
		}
		return false, "read_error"
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return true, "blank_user_md"
	}
	if strings.Contains(trimmed, "No user profile override has been hydrated") {
		return true, "placeholder_user_md"
	}
	// On first-time workspace boot, USER.md usually comes from a generic template.
	// Refresh with current room metadata so kid profile fields are present immediately.
	if firstTimeWorkspace {
		return true, "first_time_workspace_existing_user_md"
	}
	if !userProfileHasChildDetails(trimmed) {
		return true, "missing_child_profile_fields"
	}

	return false, "existing_user_profile"
}

func extractTimezoneFromUserMarkdown(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "- timezone:") {
			idx := strings.Index(trimmed, ":")
			if idx == -1 {
				return ""
			}
			return strings.TrimSpace(trimmed[idx+1:])
		}
	}
	return ""
}

func upsertUserTimezoneInUserMarkdown(content, desiredTimezone string) (updated string, changed bool, reason string) {
	desiredTimezone = strings.TrimSpace(desiredTimezone)
	if desiredTimezone == "" {
		return content, false, "timezone_empty"
	}

	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	additionalNotesIdx := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Additional Notes") {
			additionalNotesIdx = i
			break
		}
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "- timezone:") {
			current := extractTimezoneFromUserMarkdown(trimmed)
			if strings.EqualFold(current, desiredTimezone) {
				return content, false, "timezone_unchanged"
			}
			lines[i] = "- Timezone: " + desiredTimezone
			return strings.Join(lines, "\n"), true, "timezone_changed"
		}
	}

	insertAt := len(lines)
	if additionalNotesIdx >= 0 {
		insertAt = additionalNotesIdx
	}
	newLine := "- Timezone: " + desiredTimezone
	lines = append(lines[:insertAt], append([]string{newLine}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n"), true, "timezone_missing"
}

func syncUserTimezoneInFile(userPath, desiredTimezone string) (bool, string, error) {
	data, err := os.ReadFile(userPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "user_md_missing", nil
		}
		return false, "read_error", err
	}
	updated, changed, reason := upsertUserTimezoneInUserMarkdown(string(data), desiredTimezone)
	if !changed {
		return false, reason, nil
	}
	if err := os.WriteFile(userPath, []byte(ensureTrailingNewline(updated)), 0o644); err != nil {
		return false, "write_error", err
	}
	return true, reason, nil
}

func formatRoomMetadataMemoryContent(md roomMetadata) string {
	childName := strings.TrimSpace(md.ChildProfile.Name)
	var sb strings.Builder
	for _, memory := range md.LongTermMemories {
		memory = strings.TrimSpace(memory)
		if memory == "" {
			continue
		}
		if isChildIdentityMemoryLine(memory, childName) {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(memory)
		sb.WriteString("\n")
	}
	for _, relation := range md.MemoryRelations {
		source := strings.TrimSpace(relation.Source)
		rel := strings.TrimSpace(relation.Relation)
		target := strings.TrimSpace(relation.Target)
		if source == "" || rel == "" || target == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(source)
		sb.WriteString(" ")
		sb.WriteString(rel)
		sb.WriteString(" ")
		sb.WriteString(target)
		sb.WriteString("\n")
	}
	for _, entity := range md.MemoryEntities {
		name := strings.TrimSpace(entity.Name)
		if name == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(name)
		if entityType := strings.TrimSpace(entity.Type); entityType != "" {
			sb.WriteString(" (")
			sb.WriteString(entityType)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func validateLiveKitActiveSkills(workspace string, activeSkills []string) (installed []string, missing []string) {
	seen := map[string]struct{}{}
	for _, skill := range activeSkills {
		name := strings.TrimSpace(skill)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		skillPath := filepath.Join(workspace, "skills", name, "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			installed = append(installed, name)
		} else {
			missing = append(missing, name)
		}
	}
	return installed, missing
}
