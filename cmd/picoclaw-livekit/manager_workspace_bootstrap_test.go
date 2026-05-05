package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestFetchManagerWorkspaceBootstrapUsesServiceKeyAndMapsHydrationOptions(t *testing.T) {
	var gotServiceKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotServiceKey = r.Header.Get("X-Service-Key")
		if r.URL.Path != "/toy/agent/device/00:16:3e:ac:b5:38/bootstrap" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("includeMemories") != "true" {
			t.Fatalf("includeMemories = %q, want true", r.URL.Query().Get("includeMemories"))
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"bootstrapSource": "manager_api_fallback",
				"agent": map[string]any{
					"agentName":     "Cheeko",
					"systemPrompt":  "Be playful and kind.",
					"summaryMemory": "Rahul likes songs.",
					"language":      "Hindi",
				},
				"childProfile": map[string]any{
					"name":      "Rahul",
					"nickname":  "Rahu",
					"gender":    "boy",
					"interests": []string{"flowers", "music"},
					"language":  "hi",
					"timezone":  "Asia/Kolkata",
				},
				"memories": map[string]any{
					"memories": []map[string]any{
						{"memory": "Rahul likes sunflowers.", "memoryType": "fact"},
					},
					"relations": []map[string]any{
						{"source": "Rahul", "relation": "likes", "target": "music"},
					},
					"entities": []map[string]any{
						{"name": "Cheeko", "type": "assistant"},
					},
				},
				"recentSessions": []map[string]any{
					{
						"sessionId":    "session-1",
						"status":       "ended",
						"startedAt":    "2026-04-23T10:00:00Z",
						"endedAt":      "2026-04-23T10:05:00Z",
						"messageCount": 4,
					},
				},
				"sessionSummaries": []map[string]any{
					{
						"sessionId":          "session-1",
						"summary":            "Rahul asked for a flower song.",
						"sourceMessageCount": 4,
						"startedAt":          "2026-04-23T10:00:00Z",
						"endedAt":            "2026-04-23T10:05:00Z",
					},
				},
				"recentMessages": []map[string]any{
					{"sessionId": "session-1", "role": "user", "content": "Sing a flower song.", "createdAt": "2026-04-23T10:01:00Z"},
					{"sessionId": "session-1", "role": "assistant", "content": "Here is a tiny flower song.", "createdAt": "2026-04-23T10:01:03Z"},
				},
			},
		})
	}))
	defer server.Close()

	bootstrap, err := fetchManagerWorkspaceBootstrap(context.Background(), config.LiveKitServiceManagerAPIConfig{
		BaseURL:     server.URL + "/toy",
		RecentLimit: 7,
	}, "00:16:3e:ac:b5:38", "secret")
	if err != nil {
		t.Fatalf("fetchManagerWorkspaceBootstrap returned error: %v", err)
	}
	if gotServiceKey != "secret" {
		t.Fatalf("X-Service-Key = %q, want secret", gotServiceKey)
	}

	opts := buildLiveKitWorkspaceHydrationOptionsFromManager("C:\\base\\workspace", bootstrap)

	for _, want := range []string{"Cheeko", "Rahul", "Hindi"} {
		if !strings.Contains(opts.IdentityContent, want) {
			t.Fatalf("IdentityContent missing %q: %q", want, opts.IdentityContent)
		}
	}
	for _, want := range []string{"Rahul", "Nickname: Rahu", "Interests: flowers, music", "Timezone: Asia/Kolkata"} {
		if !strings.Contains(opts.UserContent, want) {
			t.Fatalf("UserContent missing %q: %q", want, opts.UserContent)
		}
	}
	for _, want := range []string{
		"Rahul likes songs",
		"Rahul likes sunflowers",
		"Rahul likes music",
		"Cheeko (assistant)",
		"Recent Session Summaries",
		"session-1",
		"Rahul asked for a flower song",
		"2026-04-23 10:00 UTC",
		"Recent Sessions",
		"4 messages",
	} {
		if !strings.Contains(opts.MemoryContent, want) {
			t.Fatalf("MemoryContent missing %q: %q", want, opts.MemoryContent)
		}
	}
	for _, want := range []string{
		"Recent Voice Messages",
		"Sing a flower song",
		"Here is a tiny flower song",
		"2026-04-23T10:01:03Z",
	} {
		if !strings.Contains(opts.SessionContextContent, want) {
			t.Fatalf("SessionContextContent missing %q: %q", want, opts.SessionContextContent)
		}
	}
}

func TestFetchManagerPromptConfigUsesPublicPromptEndpoint(t *testing.T) {
	var gotPath string
	var gotServiceKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotServiceKey = r.Header.Get("X-Service-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"agentName":    "Cheeko",
				"systemPrompt": "Use DB prompt always.",
			},
		})
	}))
	defer server.Close()

	prompt, err := fetchManagerPromptConfig(context.Background(), config.LiveKitServiceManagerAPIConfig{
		BaseURL: server.URL + "/toy",
	}, "28:56:2F:07:CC:DC")
	if err != nil {
		t.Fatalf("fetchManagerPromptConfig returned error: %v", err)
	}
	if gotPath != "/toy/agent/prompt/28:56:2F:07:CC:DC" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotServiceKey != "" {
		t.Fatalf("X-Service-Key should be empty, got %q", gotServiceKey)
	}
	if prompt.AgentName != "Cheeko" {
		t.Fatalf("AgentName = %q, want Cheeko", prompt.AgentName)
	}
	if prompt.SystemPrompt != "Use DB prompt always." {
		t.Fatalf("SystemPrompt = %q", prompt.SystemPrompt)
	}
}

func TestMergeManagerHydrationOptionsPreservesDBPromptAndAddsMemory(t *testing.T) {
	current := liveKitWorkspaceHydrationOptions{
		IdentityContent: "DB prompt",
	}
	manager := managerWorkspaceBootstrap{}
	manager.Agent.SummaryMemory = "Asha likes astronomy."
	manager.RecentMessages = []managerWorkspaceRecentMessage{
		{
			SessionID: "session-1",
			Role:      "user",
			Content:   "What did we discuss yesterday?",
			CreatedAt: "2026-04-23T10:01:00Z",
		},
	}

	got := mergeManagerHydrationOptions(current, manager, "")

	if got.IdentityContent != "DB prompt" {
		t.Fatalf("IdentityContent = %q, want DB prompt", got.IdentityContent)
	}
	if !strings.Contains(got.MemoryContent, "Asha likes astronomy") {
		t.Fatalf("MemoryContent missing manager memory: %q", got.MemoryContent)
	}
	if !strings.Contains(got.SessionContextContent, "What did we discuss yesterday?") {
		t.Fatalf("SessionContextContent missing manager recent message: %q", got.SessionContextContent)
	}
}

func TestFormatManagerMemoryContentKeepsMemoryCurated(t *testing.T) {
	bootstrap := managerWorkspaceBootstrap{}
	bootstrap.Agent.SummaryMemory = "Overall memory:\n- Rahul is the child using this device.\n- Last session highlights: noisy old session note.\nSession summary:\nBad raw text\nTranscript excerpt:\nUser: [System Event] connected"
	bootstrap.SessionSummaries = []managerWorkspaceSessionSummary{{
		SessionID:          "s1",
		Summary:            "Rahul asked about octopuses and Venus acid rain.",
		StartedAt:          "2026-04-24T12:38:46.389Z",
		EndedAt:            "2026-04-24T12:42:30.000Z",
		Status:             "ended",
		SourceMessageCount: 12,
	}}
	bootstrap.RecentSessions = []managerWorkspaceRecentSession{{
		SessionID:    "s1",
		Status:       "ended",
		StartedAt:    "2026-04-24T12:38:46.389Z",
		EndedAt:      "2026-04-24T12:42:30.000Z",
		MessageCount: 12,
	}}

	got := formatManagerMemoryContent(bootstrap)

	for _, want := range []string{"## Stable Memory", "Rahul is the child using this device", "## Recent Session Summaries", "2026-04-24", "12 messages"} {
		if !strings.Contains(got, want) {
			t.Fatalf("memory content missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"Transcript excerpt", "[System Event]", "ended started", "Session summary:\nBad raw text", "Last session highlights"} {
		if strings.Contains(got, bad) {
			t.Fatalf("memory content contains noisy text %q:\n%s", bad, got)
		}
	}
}

func TestFormatManagerSessionContextContentFiltersSystemEvents(t *testing.T) {
	bootstrap := managerWorkspaceBootstrap{
		RecentMessages: []managerWorkspaceRecentMessage{
			{SessionID: "s1", Role: "user", Content: "[System Event] The user has successfully connected to the room.", CreatedAt: "2026-04-24T01:00:00Z"},
			{SessionID: "s1", Role: "user", Content: "Do you remember yesterday?", CreatedAt: "2026-04-24T01:01:00Z"},
			{SessionID: "s1", Role: "assistant", Content: "Yes, we talked about octopuses.", CreatedAt: "2026-04-24T01:01:05Z"},
			{SessionID: "s1", Role: "tool", Content: "tool noise", CreatedAt: "2026-04-24T01:01:06Z"},
		},
	}

	got := formatManagerSessionContextContent(bootstrap)

	if !strings.Contains(got, "Do you remember yesterday?") || !strings.Contains(got, "we talked about octopuses") {
		t.Fatalf("recent context missing real dialogue:\n%s", got)
	}
	for _, bad := range []string{"[System Event]", "successfully connected", "tool noise"} {
		if strings.Contains(got, bad) {
			t.Fatalf("recent context contains %q:\n%s", bad, got)
		}
	}
}
