package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type liveKitWorkspaceStartupBootstrapResult struct {
	Workspace      string
	SkillsCopied   int
	SeededFiles    int
	ExistingSkills int
}

func ensureLiveKitDefaultWorkspaceTemplate(cfg *config.Config) (liveKitWorkspaceStartupBootstrapResult, error) {
	if cfg == nil {
		return liveKitWorkspaceStartupBootstrapResult{}, fmt.Errorf("config is nil")
	}
	workspace := strings.TrimSpace(cfg.WorkspacePath())
	if workspace == "" {
		return liveKitWorkspaceStartupBootstrapResult{}, fmt.Errorf("agents.defaults.workspace is empty")
	}
	templateSources := liveKitWorkspaceTemplateDirs(workspace)
	skillSources := liveKitSkillSourceDirs(workspace)
	return ensureLiveKitWorkspaceTemplateFromSources(workspace, templateSources, skillSources)
}

func ensureLiveKitWorkspaceTemplateFromSources(
	workspace string,
	templateSources []string,
	skillSources []string,
) (liveKitWorkspaceStartupBootstrapResult, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return liveKitWorkspaceStartupBootstrapResult{}, fmt.Errorf("workspace is empty")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0o755); err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}

	before := countNonBlankCoreTemplateFiles(workspace)
	if err := seedWorkspaceCoreFilesFromSources(workspace, templateSources); err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}
	after := countNonBlankCoreTemplateFiles(workspace)
	seeded := after - before
	if seeded < 0 {
		seeded = 0
	}

	existingSkills, err := countWorkspaceSkills(filepath.Join(workspace, "skills"))
	if err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}
	skillsCopied := 0
	if existingSkills == 0 {
		skillsCopied, err = copyWorkspaceSkillsFromSources(skillSources, filepath.Join(workspace, "skills"))
		if err != nil {
			return liveKitWorkspaceStartupBootstrapResult{}, err
		}
	}

	missingOrBlank, err := requiredWorkspaceTemplateFilesMissingOrBlank(workspace)
	if err != nil {
		return liveKitWorkspaceStartupBootstrapResult{}, err
	}
	if len(missingOrBlank) > 0 {
		return liveKitWorkspaceStartupBootstrapResult{}, fmt.Errorf(
			"default workspace template is incomplete at %s; missing_or_blank=%s; template_sources=%s",
			workspace,
			strings.Join(missingOrBlank, ","),
			strings.Join(cleanUniquePaths(templateSources), ","),
		)
	}

	return liveKitWorkspaceStartupBootstrapResult{
		Workspace:      workspace,
		SkillsCopied:   skillsCopied,
		SeededFiles:    seeded,
		ExistingSkills: existingSkills,
	}, nil
}

func countNonBlankCoreTemplateFiles(workspace string) int {
	count := 0
	for rel := range workspaceTemplateFiles {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) != "" {
			count++
		}
	}
	return count
}

func requiredWorkspaceTemplateFilesMissingOrBlank(workspace string) ([]string, error) {
	missing := make([]string, 0, len(workspaceTemplateFiles))
	for rel := range workspaceTemplateFiles {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, rel)
				continue
			}
			return nil, err
		}
		if strings.TrimSpace(string(data)) == "" {
			missing = append(missing, rel)
		}
	}
	return missing, nil
}
