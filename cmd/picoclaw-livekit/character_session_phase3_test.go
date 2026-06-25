package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestParseRoomMetadataBootstrapReadsCharacterIDAndLanguage(t *testing.T) {
	raw := `{"character_id":"char-uuid","language":"German","child_profile":{"name":"Asha","age":7}}`
	bootstrap, err := parseRoomMetadataBootstrap(raw)
	if err != nil {
		t.Fatalf("parseRoomMetadataBootstrap error: %v", err)
	}
	if bootstrap.Metadata.CharacterID != "char-uuid" {
		t.Fatalf("CharacterID = %q, want char-uuid", bootstrap.Metadata.CharacterID)
	}
	if bootstrap.Metadata.Language != "German" {
		t.Fatalf("Language = %q, want German", bootstrap.Metadata.Language)
	}
}

func TestFetchManagerCharacterSessionDecodesContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Service-Key"); got != "secret" {
			t.Errorf("X-Service-Key = %q, want secret", got)
		}
		if !strings.Contains(r.URL.Path, "/character/char-uuid/session") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"characterId":"char-uuid","characterName":"Cheeko","runtimeAgentName":"cheeko-agent1","language":"English","systemPrompt":"You are Cheeko.","soul":"I am warm."}}`))
	}))
	defer server.Close()

	out, err := fetchManagerCharacterSession(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: server.URL},
		"char-uuid",
		"secret",
	)
	if err != nil {
		t.Fatalf("fetchManagerCharacterSession error: %v", err)
	}
	if out.SystemPrompt != "You are Cheeko." || out.Soul != "I am warm." {
		t.Fatalf("unexpected persona: %+v", out)
	}
	if out.RuntimeAgentName != "cheeko-agent1" || out.Language != "English" {
		t.Fatalf("unexpected routing fields: %+v", out)
	}
}

func TestInjectLanguageFillsSlotWithDefault(t *testing.T) {
	scaffold := "Respond in: <!-- LANGUAGE -->."
	if got := injectLanguage(scaffold, "Tamil"); got != "Respond in: Tamil." {
		t.Fatalf("language not injected: %q", got)
	}
	if got := injectLanguage(scaffold, ""); got != "Respond in: English." {
		t.Fatalf("empty language should default to English: %q", got)
	}
}

func TestInjectPersonaFillsAndStripsPlaceholder(t *testing.T) {
	scaffold := "intro\n\n<!-- PERSONA -->\n\n## Role\nbody"
	if got := injectPersona(scaffold, "You are Cheeko."); !strings.Contains(got, "You are Cheeko.") ||
		strings.Contains(got, personaPlaceholder) {
		t.Fatalf("persona not injected: %q", got)
	}
	if got := injectPersona(scaffold, ""); strings.Contains(got, personaPlaceholder) {
		t.Fatalf("empty persona should strip placeholder: %q", got)
	}
}

// hydration: persona resolved -> AGENT.md = scaffold+systemPrompt and SOUL.md = soul, every session.
func TestHydratePersonaRegeneratesAgentAndSoul(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "AGENT.md"), "intro\n\n<!-- PERSONA -->\n\n## Role\nshared rules\n")
	mustWrite(t, filepath.Join(src, "SOUL.md"), "# Soul\n\nplaceholder soul\n")

	ws := t.TempDir()
	opts := liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs:  []string{src},
		PersonaSystemPrompt: "You are Cheeko, a playful buddy.",
		SoulContent:         "I am Cheeko: warm and witty.",
		RegeneratePersona:   true,
		FirstTimeWorkspace:  true,
	}
	if _, err := hydrateLiveKitWorkspaceSkeleton(ws, opts); err != nil {
		t.Fatalf("hydrate error: %v", err)
	}

	agent := mustRead(t, filepath.Join(ws, "AGENT.md"))
	if !strings.Contains(agent, "You are Cheeko, a playful buddy.") {
		t.Fatalf("AGENT.md missing persona: %q", agent)
	}
	if !strings.Contains(agent, "## Role") {
		t.Fatalf("AGENT.md missing shared scaffold: %q", agent)
	}
	if strings.Contains(agent, personaPlaceholder) {
		t.Fatalf("AGENT.md still has raw placeholder: %q", agent)
	}
	if soul := mustRead(t, filepath.Join(ws, "SOUL.md")); strings.TrimSpace(soul) != "I am Cheeko: warm and witty." {
		t.Fatalf("SOUL.md = %q, want manager soul", soul)
	}
}

// full AGENT.md in system_prompt (discriminated by <!-- LANGUAGE -->): use it verbatim,
// never merge the on-disk scaffold; only the language slot gets filled.
func TestHydrateFullAgentMdFromSystemPromptBypassesScaffold(t *testing.T) {
	src := t.TempDir()
	// Scaffold carries its own PERSONA marker; the full AGENT.md path must not use it.
	mustWrite(t, filepath.Join(src, "AGENT.md"), "SCAFFOLD INTRO\n\n<!-- PERSONA -->\n\n## Role\nscaffold rules\n")
	mustWrite(t, filepath.Join(src, "SOUL.md"), "# Soul\n\nplaceholder soul\n")

	fullAgentMd := "# Cheeko\n\nYou are Cheeko.\n\n" +
		"## Child-Safety Rules\nBe kind and safe.\n\n" +
		"## Runtime Guardrails\nStay in character.\n\n" +
		"Respond in: <!-- LANGUAGE -->.\n"

	ws := t.TempDir()
	opts := liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs:  []string{src},
		PersonaSystemPrompt: fullAgentMd,
		SessionLanguage:     "Tamil",
		RegeneratePersona:   true,
		FirstTimeWorkspace:  true,
	}
	if _, err := hydrateLiveKitWorkspaceSkeleton(ws, opts); err != nil {
		t.Fatalf("hydrate error: %v", err)
	}

	agent := mustRead(t, filepath.Join(ws, "AGENT.md"))
	if !strings.Contains(agent, "## Child-Safety Rules") {
		t.Fatalf("full AGENT.md missing its safety rules: %q", agent)
	}
	if !strings.Contains(agent, "Respond in: Tamil.") {
		t.Fatalf("language slot not filled: %q", agent)
	}
	if strings.Contains(agent, languagePlaceholder) {
		t.Fatalf("language placeholder still present: %q", agent)
	}
	if strings.Contains(agent, personaPlaceholder) {
		t.Fatalf("full AGENT.md must not carry scaffold's PERSONA marker: %q", agent)
	}
	if strings.Contains(agent, "SCAFFOLD INTRO") || strings.Contains(agent, "scaffold rules") {
		t.Fatalf("full AGENT.md must not be merged with the scaffold: %q", agent)
	}
}

// legacy persona snippet (no <!-- LANGUAGE -->): still injected into the on-disk scaffold.
func TestHydrateLegacyPersonaSnippetUsesScaffold(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "AGENT.md"), "intro\n\n<!-- PERSONA -->\n\n## Role\nshared rules\n")
	mustWrite(t, filepath.Join(src, "SOUL.md"), "# Soul\n\nplaceholder soul\n")

	ws := t.TempDir()
	opts := liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs:  []string{src},
		PersonaSystemPrompt: "You are Cheeko, a playful buddy.", // legacy snippet, no language slot
		RegeneratePersona:   true,
		FirstTimeWorkspace:  true,
	}
	if _, err := hydrateLiveKitWorkspaceSkeleton(ws, opts); err != nil {
		t.Fatalf("hydrate error: %v", err)
	}

	agent := mustRead(t, filepath.Join(ws, "AGENT.md"))
	if !strings.Contains(agent, "You are Cheeko, a playful buddy.") {
		t.Fatalf("legacy persona not injected: %q", agent)
	}
	if !strings.Contains(agent, "## Role") || !strings.Contains(agent, "shared rules") {
		t.Fatalf("legacy path must come from the scaffold: %q", agent)
	}
	if strings.Contains(agent, personaPlaceholder) {
		t.Fatalf("legacy path left raw PERSONA placeholder: %q", agent)
	}
}

// degraded (Manager down): keep the last-rendered AGENT.md/SOUL.md, do not overwrite.
func TestHydrateDegradedKeepsLastRendered(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "AGENT.md"), "intro\n\n<!-- PERSONA -->\n\n## Role\nshared\n")
	mustWrite(t, filepath.Join(src, "SOUL.md"), "# Soul\n\nplaceholder\n")

	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "AGENT.md"), "LAST GOOD CHEEKO PROMPT\n")
	mustWrite(t, filepath.Join(ws, "SOUL.md"), "LAST GOOD CHEEKO SOUL\n")
	mustWrite(t, filepath.Join(ws, "USER.md"), "# User\n\nexisting\n") // not first-time

	opts := liveKitWorkspaceHydrationOptions{
		TemplateSourceDirs: []string{src},
		RegeneratePersona:  false, // persona pull failed
	}
	if _, err := hydrateLiveKitWorkspaceSkeleton(ws, opts); err != nil {
		t.Fatalf("hydrate error: %v", err)
	}
	if got := mustRead(t, filepath.Join(ws, "AGENT.md")); !strings.Contains(got, "LAST GOOD CHEEKO PROMPT") {
		t.Fatalf("degraded AGENT.md was overwritten: %q", got)
	}
	if got := mustRead(t, filepath.Join(ws, "SOUL.md")); !strings.Contains(got, "LAST GOOD CHEEKO SOUL") {
		t.Fatalf("degraded SOUL.md was overwritten: %q", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
