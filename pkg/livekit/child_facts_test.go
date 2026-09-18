package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/livekit/protocol/livekit"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestRestoreChildFactsWritesStateFileAndClearsItWithoutAChild(t *testing.T) {
	reply := `{"code":0,"data":{"kidId":"77","facts":[{"category":"pet","subject":"dog","fact":"Has a dog named Bruno"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/device/aa:bb:cc:dd:ee:ff/facts" || r.URL.Query().Get("limit") != "20" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		_, _ = w.Write([]byte(reply))
	}))
	defer server.Close()

	workspace := t.TempDir()
	path := filepath.Join(stateDir(workspace), ChildFactsStateFile)

	RestoreChildFacts(context.Background(), server.URL, "secret", "aa:bb:cc:dd:ee:ff", workspace)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "- Has a dog named Bruno") {
		t.Fatalf("state file = %q, err = %v", data, err)
	}

	// The toy is unpaired (or handed to a sibling): the previous facts must go.
	reply = `{"code":0,"data":{"kidId":null,"facts":[]}}`
	RestoreChildFacts(context.Background(), server.URL, "secret", "aa:bb:cc:dd:ee:ff", workspace)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file still present after unpairing: %v", err)
	}
}

func TestParseChildFactsFindsArrayInsideProseAndDropsEmptyItems(t *testing.T) {
	reply := "Sure!\n```json\n[{\"category\":\"pet\",\"subject\":\"dog\",\"fact\":\"Has a dog named Bruno\"},{\"category\":\"likes\",\"subject\":\"\",\"fact\":\"x\"}]\n```"
	facts, err := parseChildFacts(reply)
	if err != nil {
		t.Fatalf("parseChildFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].Subject != "dog" || facts[0].Fact != "Has a dog named Bruno" {
		t.Fatalf("facts = %+v", facts)
	}
	if _, err := parseChildFacts("nothing here"); err == nil {
		t.Fatalf("want error for a reply with no array")
	}
}

// capturingFactsProvider returns a fixed facts reply and records the prompt.
type capturingFactsProvider struct{ prompt *string }

func (p capturingFactsProvider) Chat(_ context.Context, msgs []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	*p.prompt = msgs[0].Content
	return &providers.LLMResponse{Content: `[{"category":"pet","subject":"dog","fact":"Has a dog named Bruno"}]`}, nil
}

func (capturingFactsProvider) GetDefaultModel() string { return "test-model" }

func TestPersistChildFactsSendsExtractedFactsWithKnownFactsInPrompt(t *testing.T) {
	var sent map[string][]childFact
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "secret" {
			t.Fatalf("X-Service-Key header = %q", r.Header.Get("X-Service-Key"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agent/device/aa:bb:cc:dd:ee:ff/facts":
			_, _ = w.Write([]byte(`{"code":0,"data":{"kidId":"77","facts":[{"category":"family","subject":"sister","fact":"Has a sister, Meera"}]}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/agent/device/aa:bb:cc:dd:ee:ff/sessions/session-1/facts":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatalf("decode facts payload: %v", err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"saved":1,"skipped":0}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	rs := &RoomSession{
		managerAPIURL:    server.URL,
		managerAPISecret: "secret",
		deviceMAC:        "aa:bb:cc:dd:ee:ff",
		roomInfo:         &protocol.Room{Name: "session-1"},
	}
	sessions := session.NewSessionManager("")
	key := rs.sessionKeyForParticipant("")
	sessions.AddMessage(key, "user", "my dog Bruno is funny")
	sessions.AddMessage(key, "assistant", "Bruno sounds lovely!")

	var prompt string
	bridge := &AgentBridge{provider: capturingFactsProvider{prompt: &prompt}, sessions: sessions}

	rs.persistChildFacts(context.Background(), bridge)

	if !strings.Contains(prompt, "family/sister: Has a sister, Meera") {
		t.Fatalf("prompt does not list known facts:\n%s", prompt)
	}
	if !strings.Contains(prompt, "user: my dog Bruno is funny") {
		t.Fatalf("prompt does not include the conversation:\n%s", prompt)
	}
	if got := sent["facts"]; len(got) != 1 || got[0].Subject != "dog" {
		t.Fatalf("sent facts = %+v", sent)
	}
}
