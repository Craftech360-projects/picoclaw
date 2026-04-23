package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

type managerWorkspaceBootstrap struct {
	BootstrapSource string `json:"bootstrapSource"`
	Agent           struct {
		AgentName     string `json:"agentName"`
		SystemPrompt  string `json:"systemPrompt"`
		SummaryMemory string `json:"summaryMemory"`
		Language      string `json:"language"`
		LangCode      string `json:"langCode"`
	} `json:"agent"`
	ChildProfile *struct {
		Name        string   `json:"name"`
		Nickname    string   `json:"nickname"`
		Gender      string   `json:"gender"`
		Grade       string   `json:"grade"`
		School      string   `json:"school"`
		Interests   []string `json:"interests"`
		Language    string   `json:"language"`
		Timezone    string   `json:"timezone"`
		BirthDate   string   `json:"birthDate"`
		Preferences any      `json:"preferences"`
	} `json:"childProfile"`
	Memories struct {
		Memories  []managerWorkspaceMemory   `json:"memories"`
		Relations []managerWorkspaceRelation `json:"relations"`
		Entities  []managerWorkspaceEntity   `json:"entities"`
	} `json:"memories"`
}

type managerWorkspaceMemory struct {
	Memory     string `json:"memory"`
	Content    string `json:"content"`
	MemoryType string `json:"memoryType"`
	Source     string `json:"source"`
}

type managerWorkspaceRelation struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

type managerWorkspaceEntity struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type managerPromptConfig struct {
	AgentName    string `json:"agentName"`
	SystemPrompt string `json:"systemPrompt"`
}

func fetchManagerPromptConfig(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
) (managerPromptConfig, error) {
	var out managerPromptConfig
	deviceMAC = strings.TrimSpace(deviceMAC)
	if deviceMAC == "" {
		return out, fmt.Errorf("device MAC is empty")
	}

	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" {
		baseURL = "http://localhost:8002/toy"
	}

	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/prompt/" + url.PathEscape(deviceMAC)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return out, err
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("manager prompt status=%d body=%s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return out, fmt.Errorf("decode manager prompt response: %w", err)
	}
	if wrapper.Code != 0 {
		return out, fmt.Errorf("manager prompt api code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(wrapper.Data, &out); err != nil {
		return out, fmt.Errorf("decode manager prompt data: %w", err)
	}
	return out, nil
}

func fetchManagerWorkspaceBootstrap(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	serviceKey string,
) (managerWorkspaceBootstrap, error) {
	var out managerWorkspaceBootstrap
	deviceMAC = strings.TrimSpace(deviceMAC)
	if deviceMAC == "" {
		return out, fmt.Errorf("device MAC is empty")
	}

	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" {
		baseURL = "http://localhost:8002/toy"
	}
	recentLimit := cfg.RecentLimit
	if recentLimit <= 0 {
		recentLimit = 50
	}

	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/device/" + url.PathEscape(deviceMAC) +
		"/bootstrap?includeMemories=true&recentLimit=" + strconv.Itoa(recentLimit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(serviceKey) != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("manager bootstrap status=%d body=%s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return out, fmt.Errorf("decode manager bootstrap response: %w", err)
	}
	if wrapper.Code != 0 {
		return out, fmt.Errorf("manager bootstrap api code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(wrapper.Data, &out); err != nil {
		return out, fmt.Errorf("decode manager bootstrap data: %w", err)
	}
	return out, nil
}

func buildLiveKitWorkspaceHydrationOptionsFromManager(
	baseWorkspace string,
	bootstrap managerWorkspaceBootstrap,
) liveKitWorkspaceHydrationOptions {
	opts := liveKitWorkspaceHydrationOptions{
		IdentityContent: formatManagerIdentityContent(bootstrap),
		UserContent:     formatManagerUserContent(bootstrap),
		MemoryContent:   formatManagerMemoryContent(bootstrap),
	}
	if strings.TrimSpace(baseWorkspace) != "" {
		opts.SkillsSourceDir = filepath.Join(baseWorkspace, "skills")
	}
	return opts
}

func formatManagerIdentityContent(bootstrap managerWorkspaceBootstrap) string {
	var sb strings.Builder
	sb.WriteString("# Agent\n\n")
	if agentName := strings.TrimSpace(bootstrap.Agent.AgentName); agentName != "" {
		sb.WriteString("You are ")
		sb.WriteString(agentName)
		sb.WriteString(".\n\n")
	}
	if systemPrompt := strings.TrimSpace(bootstrap.Agent.SystemPrompt); systemPrompt != "" {
		sb.WriteString(systemPrompt)
		sb.WriteString("\n\n")
	}
	if bootstrap.ChildProfile != nil && strings.TrimSpace(bootstrap.ChildProfile.Name) != "" {
		sb.WriteString("## Active Child\n\n")
		sb.WriteString("- Name: ")
		sb.WriteString(strings.TrimSpace(bootstrap.ChildProfile.Name))
		sb.WriteString("\n")
	}
	if language := firstNonEmpty(bootstrap.Agent.Language, bootstrap.Agent.LangCode); language != "" {
		sb.WriteString("- Language: ")
		sb.WriteString(language)
		sb.WriteString("\n")
	}
	if summary := strings.TrimSpace(bootstrap.Agent.SummaryMemory); summary != "" {
		sb.WriteString("\n## Summary Memory\n\n")
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func formatManagerUserContent(bootstrap managerWorkspaceBootstrap) string {
	if bootstrap.ChildProfile == nil {
		return ""
	}
	child := bootstrap.ChildProfile
	var sb strings.Builder
	sb.WriteString("# User\n\n")
	writeMarkdownField(&sb, "Name", child.Name)
	writeMarkdownField(&sb, "Nickname", child.Nickname)
	writeMarkdownField(&sb, "Gender", child.Gender)
	writeMarkdownField(&sb, "Grade", child.Grade)
	writeMarkdownField(&sb, "School", child.School)
	if len(child.Interests) > 0 {
		writeMarkdownField(&sb, "Interests", strings.Join(cleanStrings(child.Interests), ", "))
	}
	writeMarkdownField(&sb, "Language", child.Language)
	writeMarkdownField(&sb, "Timezone", child.Timezone)
	writeMarkdownField(&sb, "Birth date", child.BirthDate)
	return strings.TrimSpace(sb.String())
}

func formatManagerMemoryContent(bootstrap managerWorkspaceBootstrap) string {
	var sb strings.Builder
	if summary := strings.TrimSpace(bootstrap.Agent.SummaryMemory); summary != "" {
		sb.WriteString("- ")
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
	for _, memory := range bootstrap.Memories.Memories {
		content := firstNonEmpty(memory.Memory, memory.Content)
		if content == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(content)
		sb.WriteString("\n")
	}
	for _, relation := range bootstrap.Memories.Relations {
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
	for _, entity := range bootstrap.Memories.Entities {
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

func writeMarkdownField(sb *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	sb.WriteString("- ")
	sb.WriteString(label)
	sb.WriteString(": ")
	sb.WriteString(value)
	sb.WriteString("\n")
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
