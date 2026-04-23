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

	for _, want := range []string{"Cheeko", "Be playful and kind", "Rahul", "Hindi"} {
		if !strings.Contains(opts.IdentityContent, want) {
			t.Fatalf("IdentityContent missing %q: %q", want, opts.IdentityContent)
		}
	}
	for _, want := range []string{"Rahul", "Nickname: Rahu", "Interests: flowers, music", "Timezone: Asia/Kolkata"} {
		if !strings.Contains(opts.UserContent, want) {
			t.Fatalf("UserContent missing %q: %q", want, opts.UserContent)
		}
	}
	for _, want := range []string{"Rahul likes songs", "Rahul likes sunflowers", "Rahul likes music", "Cheeko (assistant)"} {
		if !strings.Contains(opts.MemoryContent, want) {
			t.Fatalf("MemoryContent missing %q: %q", want, opts.MemoryContent)
		}
	}
}
