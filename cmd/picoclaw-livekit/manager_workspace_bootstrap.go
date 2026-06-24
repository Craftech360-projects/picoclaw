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
	RecentMessages   []managerWorkspaceRecentMessage  `json:"recentMessages"`
	RecentSessions   []managerWorkspaceRecentSession  `json:"recentSessions"`
	SessionSummaries []managerWorkspaceSessionSummary `json:"sessionSummaries"`
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

type managerWorkspaceRecentMessage struct {
	SessionID string `json:"sessionId"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

type managerWorkspaceRecentSession struct {
	SessionID    string `json:"sessionId"`
	Status       string `json:"status"`
	StartedAt    string `json:"startedAt"`
	EndedAt      string `json:"endedAt"`
	MessageCount int    `json:"messageCount"`
}

type managerWorkspaceSessionSummary struct {
	SessionID          string `json:"sessionId"`
	Summary            string `json:"summary"`
	Model              string `json:"model"`
	SourceMessageCount int    `json:"sourceMessageCount"`
	UpdatedAt          string `json:"updatedAt"`
	StartedAt          string `json:"startedAt"`
	EndedAt            string `json:"endedAt"`
	Status             string `json:"status"`
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

// managerCharacterSession is the persona contract returned by
// GET /character/:id/session (Phase 1 resolver). No hashes (ADR-0001/0003).
type managerCharacterSession struct {
	CharacterID      string `json:"characterId"`
	CharacterName    string `json:"characterName"`
	RuntimeAgentName string `json:"runtimeAgentName"`
	Language         string `json:"language"`
	SystemPrompt     string `json:"systemPrompt"`
	Soul             string `json:"soul"`
}

// fetchManagerCharacterSession PULLs a character's persona by id (ADR-0003).
func fetchManagerCharacterSession(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	characterID string,
	serviceKey string,
) (managerCharacterSession, error) {
	var out managerCharacterSession
	characterID = strings.TrimSpace(characterID)
	if characterID == "" {
		return out, fmt.Errorf("character id is empty")
	}

	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" {
		baseURL = "http://localhost:8002/toy"
	}

	// Mounted under the /agent router on the Manager (sibling of /agent/device/:mac/...).
	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/character/" + url.PathEscape(characterID) + "/session"

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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("manager character session status=%d body=%s", resp.StatusCode, string(body))
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return out, fmt.Errorf("decode manager character session response: %w", err)
	}
	if wrapper.Code != 0 {
		return out, fmt.Errorf("manager character session api code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return out, fmt.Errorf("manager character session returned empty data")
	}
	if err := json.Unmarshal(wrapper.Data, &out); err != nil {
		return out, fmt.Errorf("decode manager character session data: %w", err)
	}
	return out, nil
}

func buildLiveKitWorkspaceHydrationOptionsFromManager(
	baseWorkspace string,
	bootstrap managerWorkspaceBootstrap,
) liveKitWorkspaceHydrationOptions {
	opts := liveKitWorkspaceHydrationOptions{
		IdentityContent:       formatManagerIdentityContent(bootstrap),
		UserContent:           formatManagerUserContent(bootstrap),
		MemoryContent:         formatManagerMemoryContent(bootstrap),
		SessionContextContent: formatManagerSessionContextContent(bootstrap),
	}
	if strings.TrimSpace(baseWorkspace) != "" {
		opts.SkillsSourceDir = filepath.Join(baseWorkspace, "skills")
		opts.SkillsSourceDirs = liveKitSkillSourceDirs(baseWorkspace)
	}
	return opts
}

func mergeManagerHydrationOptions(
	current liveKitWorkspaceHydrationOptions,
	manager managerWorkspaceBootstrap,
	baseWorkspace string,
) liveKitWorkspaceHydrationOptions {
	managerOpts := buildLiveKitWorkspaceHydrationOptionsFromManager(baseWorkspace, manager)
	if strings.TrimSpace(current.IdentityContent) == "" {
		current.IdentityContent = managerOpts.IdentityContent
	}
	if strings.TrimSpace(current.UserContent) == "" {
		current.UserContent = managerOpts.UserContent
	}
	if strings.TrimSpace(managerOpts.MemoryContent) != "" {
		current.MemoryContent = managerOpts.MemoryContent
	}
	if strings.TrimSpace(managerOpts.SessionContextContent) != "" {
		current.SessionContextContent = managerOpts.SessionContextContent
	}
	if len(current.SkillsSourceDirs) == 0 {
		current.SkillsSourceDirs = managerOpts.SkillsSourceDirs
	}
	if strings.TrimSpace(current.SkillsSourceDir) == "" {
		current.SkillsSourceDir = managerOpts.SkillsSourceDir
	}
	return current
}

func formatManagerIdentityContent(bootstrap managerWorkspaceBootstrap) string {
	var sb strings.Builder
	sb.WriteString("# Agent\n\n")
	if agentName := strings.TrimSpace(bootstrap.Agent.AgentName); agentName != "" {
		sb.WriteString("You are ")
		sb.WriteString(agentName)
		sb.WriteString(".\n\n")
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
	timezone := strings.TrimSpace(child.Timezone)
	if timezone == "" {
		timezone = "Asia/Kolkata"
	}
	writeMarkdownField(&sb, "Timezone", timezone)
	writeMarkdownField(&sb, "Birth date", child.BirthDate)
	return strings.TrimSpace(sb.String())
}

func formatManagerMemoryContent(bootstrap managerWorkspaceBootstrap) string {
	const maxStableMemoryItems = 30
	childName := ""
	if bootstrap.ChildProfile != nil {
		childName = bootstrap.ChildProfile.Name
	}

	var sb strings.Builder
	stableLines := make([]string, 0, maxStableMemoryItems)
	seenStable := map[string]struct{}{}
	addStableLines := func(lines []string) {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if isChildIdentityMemoryLine(line, childName) {
				continue
			}
			key := strings.ToLower(strings.TrimPrefix(line, "- "))
			if _, ok := seenStable[key]; ok {
				continue
			}
			seenStable[key] = struct{}{}
			stableLines = append(stableLines, line)
			if len(stableLines) >= maxStableMemoryItems {
				return
			}
		}
	}

	if summary := strings.TrimSpace(bootstrap.Agent.SummaryMemory); summary != "" {
		addStableLines(cleanManagerMemoryLines(summary))
	}
	for _, memory := range bootstrap.Memories.Memories {
		if strings.EqualFold(strings.TrimSpace(memory.MemoryType), "episode") {
			continue
		}
		content := firstNonEmpty(memory.Memory, memory.Content)
		if content == "" {
			continue
		}
		addStableLines(cleanManagerMemoryLines(content))
	}
	if len(stableLines) > 0 {
		sb.WriteString("# Memory\n\n## Stable Memory\n\n")
		for _, line := range stableLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	relationLines := make([]string, 0, len(bootstrap.Memories.Relations)+len(bootstrap.Memories.Entities))
	for _, relation := range bootstrap.Memories.Relations {
		source := strings.TrimSpace(relation.Source)
		rel := strings.TrimSpace(relation.Relation)
		target := strings.TrimSpace(relation.Target)
		if source == "" || rel == "" || target == "" {
			continue
		}
		relationLines = append(relationLines, "- "+source+" "+rel+" "+target)
	}
	for _, entity := range bootstrap.Memories.Entities {
		name := strings.TrimSpace(entity.Name)
		if name == "" {
			continue
		}
		line := "- " + name
		if entityType := strings.TrimSpace(entity.Type); entityType != "" {
			line += " (" + entityType + ")"
		}
		relationLines = append(relationLines, line)
	}
	if len(relationLines) > 0 {
		if sb.Len() == 0 {
			sb.WriteString("# Memory\n\n")
		} else {
			sb.WriteString("\n")
		}
		sb.WriteString("## Memory Graph\n\n")
		for _, line := range relationLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	writeManagerSessionSummaries(&sb, bootstrap.SessionSummaries)
	writeManagerRecentSessions(&sb, bootstrap.RecentSessions)
	return strings.TrimSpace(sb.String())
}

func cleanManagerMemoryLines(value string) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	seen := map[string]struct{}{}
	skipRawBlock := false
	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		line := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if trimmed == "" {
			if skipRawBlock {
				continue
			}
			continue
		}
		if isManagerMemoryBlockLabel(line) {
			skipRawBlock = true
			continue
		}
		if skipRawBlock {
			if isManagerMemoryNoiseLine(line) || !strings.HasPrefix(trimmed, "-") {
				continue
			}
			skipRawBlock = false
		}
		if isManagerMemoryNoiseLine(line) {
			continue
		}
		key := strings.ToLower(line)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, "- "+line)
	}
	return out
}

func isManagerMemoryBlockLabel(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	return lower == "session summary:" || lower == "transcript excerpt:"
}

func isManagerMemoryNoiseLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	return lower == "overall memory:" ||
		lower == "session summary:" ||
		lower == "transcript excerpt:" ||
		strings.Contains(lower, "is the child using this device") ||
		strings.HasPrefix(lower, "last session highlights:") ||
		strings.HasPrefix(lower, "good follow-up topics:") ||
		strings.HasPrefix(lower, "user:") ||
		strings.HasPrefix(lower, "assistant:") ||
		strings.HasPrefix(lower, "system:") ||
		strings.HasPrefix(lower, "tool:") ||
		strings.Contains(lower, "[system event]") ||
		strings.Contains(lower, "successfully connected to the room") ||
		strings.Contains(lower, "you must end this conversation now")
}

func isChildIdentityMemoryLine(line, childName string) bool {
	name := strings.ToLower(strings.TrimSpace(childName))
	if name == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, name+" is the child using this device") ||
		strings.Contains(lower, name+" is the child") ||
		strings.Contains(lower, "child name: "+name) ||
		strings.HasPrefix(lower, "name: "+name)
}

func writeManagerSessionSummaries(sb *strings.Builder, summaries []managerWorkspaceSessionSummary) {
	wroteHeader := false
	for _, item := range summaries {
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			continue
		}
		if !wroteHeader {
			if sb.Len() == 0 {
				sb.WriteString("# Memory\n\n")
			} else {
				sb.WriteString("\n")
			}
			sb.WriteString("## Recent Session Summaries\n\n")
			wroteHeader = true
		}
		sb.WriteString("- ")
		sb.WriteString(formatManagerSessionDate(item.StartedAt))
		if status := strings.TrimSpace(item.Status); status != "" {
			sb.WriteString(", ")
			sb.WriteString(status)
		}
		if item.SourceMessageCount > 0 {
			fmt.Fprintf(sb, ", %d messages", item.SourceMessageCount)
		}
		if sessionID := strings.TrimSpace(item.SessionID); sessionID != "" {
			sb.WriteString(", session ")
			sb.WriteString(sessionID)
		}
		sb.WriteString(": ")
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
}

func writeManagerRecentSessions(sb *strings.Builder, sessions []managerWorkspaceRecentSession) {
	wroteHeader := false
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.SessionID)
		if sessionID == "" {
			continue
		}
		if !wroteHeader {
			if sb.Len() == 0 {
				sb.WriteString("# Memory\n\n")
			} else {
				sb.WriteString("\n")
			}
			sb.WriteString("## Recent Sessions\n\n")
			wroteHeader = true
		}
		sb.WriteString("- ")
		sb.WriteString(formatManagerSessionDate(session.StartedAt))
		if status := strings.TrimSpace(session.Status); status != "" {
			sb.WriteString(", ")
			sb.WriteString(status)
		}
		if endedAt := strings.TrimSpace(session.EndedAt); endedAt != "" {
			sb.WriteString(", ended ")
			sb.WriteString(formatManagerSessionDate(endedAt))
		}
		if session.MessageCount > 0 {
			fmt.Fprintf(sb, ", %d messages", session.MessageCount)
		}
		sb.WriteString(", session ")
		sb.WriteString(sessionID)
		sb.WriteString("\n")
	}
}

func formatManagerSessionDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown date"
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format("2006-01-02 15:04 UTC")
	}
	return value
}

func formatManagerSessionContextContent(bootstrap managerWorkspaceBootstrap) string {
	const maxRecentMessages = 50
	const maxRecentContextBytes = 16 * 1024

	var sb strings.Builder
	written := 0
	for _, msg := range bootstrap.RecentMessages {
		if written >= maxRecentMessages {
			break
		}
		if !includeRecentVoiceMessage(msg) {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if written == 0 {
			sb.WriteString("# Recent Voice Messages\n\n")
		}
		var line strings.Builder
		line.WriteString("- ")
		if createdAt := strings.TrimSpace(msg.CreatedAt); createdAt != "" {
			line.WriteString(createdAt)
			line.WriteString(" ")
		}
		if sessionID := strings.TrimSpace(msg.SessionID); sessionID != "" {
			line.WriteString("[")
			line.WriteString(sessionID)
			line.WriteString("] ")
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		line.WriteString(role)
		line.WriteString(": ")
		line.WriteString(content)
		line.WriteString("\n")
		if sb.Len()+line.Len() > maxRecentContextBytes {
			break
		}
		sb.WriteString(line.String())
		written++
	}
	return strings.TrimSpace(sb.String())
}

func includeRecentVoiceMessage(msg managerWorkspaceRecentMessage) bool {
	role := strings.ToLower(strings.TrimSpace(msg.Role))
	if role != "user" && role != "assistant" {
		return false
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return false
	}
	lower := strings.ToLower(content)
	return !strings.Contains(lower, "[system event]") &&
		!strings.Contains(lower, "successfully connected to the room") &&
		!strings.Contains(lower, "you must end this conversation now") &&
		!strings.Contains(lower, "shutdown")
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
