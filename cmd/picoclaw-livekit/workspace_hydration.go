package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/config"
)

type liveKitWorkspaceHydrationOptions struct {
	IdentityContent       string
	UserContent           string
	MemoryContent         string
	SessionContextContent string
	TemplateSourceDir     string
	TemplateSourceDirs    []string
	SkillsSourceDir       string
	SkillsSourceDirs      []string
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

	identityContent := strings.TrimSpace(opts.IdentityContent)
	if identityContent != "" {
		if err := writeFileIfMissingOrBlank(
			filepath.Join(workspace, "AGENT.md"),
			ensureTrailingNewline(identityContent),
			0o644,
		); err != nil {
			return result, err
		}
	} else {
		placeholder := "# LiveKit Voice Agent\n\nNo room identity has been hydrated for this session.\n"
		if err := writeFileIfMissingOrBlank(filepath.Join(workspace, "AGENT.md"), placeholder, 0o644); err != nil {
			return result, err
		}
	}

	userContent := strings.TrimSpace(opts.UserContent)
	if userContent != "" {
		if err := os.WriteFile(filepath.Join(workspace, "USER.md"), []byte(ensureTrailingNewline(userContent)), 0o644); err != nil {
			return result, err
		}
	} else {
		if err := writeFileIfMissingOrBlank(filepath.Join(workspace, "USER.md"), "# User\n\nNo user profile override has been hydrated for this session.\n", 0o644); err != nil {
			return result, err
		}
	}
	if err := writeFileIfMissingOrBlank(filepath.Join(workspace, "SOUL.md"), "# Soul\n\nUse the active LiveKit room identity and child context for this voice session.\n", 0o644); err != nil {
		return result, err
	}
	if err := writeFileIfMissingOrBlank(filepath.Join(workspace, "HEARTBEAT.md"), "# Heartbeat\n\nLiveKit workspace hydrated.\n", 0o644); err != nil {
		return result, err
	}
	if err := writeFileIfMissing(filepath.Join(workspace, "heartbeat.log"), "", 0o644); err != nil {
		return result, err
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	memoryContent := strings.TrimSpace(opts.MemoryContent)
	switch {
	case memoryContent != "":
		if !strings.HasPrefix(memoryContent, "#") {
			memoryContent = "# Memory\n\n" + memoryContent
		}
		if err := os.WriteFile(memoryPath, []byte(ensureTrailingNewline(memoryContent)), 0o600); err != nil {
			return result, err
		}
		result.MemoryWritten = true
	default:
		if err := writeFileIfMissingOrBlank(memoryPath, "# Memory\n\nNo durable memory has been hydrated yet.\n", 0o600); err != nil {
			return result, err
		}
	}

	sessionContextContent := strings.TrimSpace(opts.SessionContextContent)
	if sessionContextContent != "" {
		if err := os.WriteFile(
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
			if err := os.WriteFile(target, []byte(ensureTrailingNewline(string(data))), spec.Perm); err != nil {
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
	return os.WriteFile(path, []byte(content), perm)
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
	return os.WriteFile(path, []byte(content), perm)
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
	return strings.TrimSpace(sb.String())
}

func formatRoomMetadataUserContent(md roomMetadata) string {
	var sb strings.Builder
	sb.WriteString("# User\n\n")
	if name := strings.TrimSpace(md.ChildProfile.Name); name != "" {
		sb.WriteString("- Name: ")
		sb.WriteString(name)
		sb.WriteString("\n")
	}
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

func formatRoomMetadataMemoryContent(md roomMetadata) string {
	var sb strings.Builder
	for _, memory := range md.LongTermMemories {
		memory = strings.TrimSpace(memory)
		if memory == "" {
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
