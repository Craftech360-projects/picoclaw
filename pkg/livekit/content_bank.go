package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Content banks for the UNSCORED characters (Masti, Tara, Mitthu, Nani, Tikku).
// Same speculative-fetch pattern as the quiz batch, but content only: there is
// no verdict, no answer POST and no level derivation — progression and
// no-repeat live in the character's MEMO (persisted via character_progress.go).
// A missing payload is never an error: every one of these prompts carries a
// STARTER-MODE branch.

// Content placeholders, one per character's greeting. All render from the same
// payload — a session serves exactly one character, so at most one appears.
var contentPlaceholders = []string{
	"{{JOKES}}", "{{WHY_QUESTIONS}}", "{{WORDS_OF_THE_DAY}}",
	"{{STORY_OF_THE_DAY}}", "{{SPELL_WORDS}}",
}

// ContentPayload is one session's serving from GET /content-bank/next.
// Items keep the API's column names; rendering picks what it knows and skips
// the rest, so an admin adding a column never breaks a session.
type ContentPayload struct {
	Bank  string           `json:"bank"`
	Items []map[string]any `json:"items"`
}

// PromptWantsContentBank mirrors PromptWantsQuizBatch for the content
// placeholders. Same rule: callers MUST use this, never test a literal.
func PromptWantsContentBank(prompt string) bool {
	for _, p := range contentPlaceholders {
		if strings.Contains(prompt, p) {
			return true
		}
	}
	return false
}

// FetchContentBank PULLs the session's content. A character with no content
// bank comes back as (nil, nil) — distinct from an unreachable API.
func FetchContentBank(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	characterName string,
) (*ContentPayload, error) {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" {
		return nil, fmt.Errorf("device mac is empty")
	}
	endpoint := managerQuizBaseURL(cfg) + "/content-bank/next?device_mac=" + url.QueryEscape(deviceMac)
	if name := strings.TrimSpace(characterName); name != "" {
		endpoint += "&character=" + url.QueryEscape(name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setQuizServiceKey(req, serviceKey)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("content-bank status=%d body=%s", resp.StatusCode, string(body))
	}
	data, err := unwrapQuizEnvelope(body, "content-bank next")
	if err != nil {
		return nil, err
	}
	var payload ContentPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode content-bank data: %w", err)
	}
	if strings.TrimSpace(payload.Bank) == "" || len(payload.Items) == 0 {
		return nil, nil // character has no content bank
	}
	return &payload, nil
}

// RenderContentBank substitutes any content placeholder in a greeting. Prompts
// without one are untouched; a nil payload consumes the placeholder with the
// explicit STARTER-MODE trigger (a raw {{TAG}} would be read aloud).
func RenderContentBank(prompt string, payload *ContentPayload) string {
	if !PromptWantsContentBank(prompt) {
		return prompt
	}
	block := contentBlock(payload)
	for _, p := range contentPlaceholders {
		prompt = strings.ReplaceAll(prompt, p, block)
	}
	return prompt
}

func contentBlock(payload *ContentPayload) string {
	if payload == nil || len(payload.Items) == 0 {
		// The prompts' own instruction: a missing/empty list means STARTER MODE.
		return "(no list supplied today - run STARTER MODE)"
	}
	var b strings.Builder
	switch payload.Bank {
	case "joke":
		b.WriteString("## Today's Jokes\n")
		for _, it := range payload.Items {
			fmt.Fprintf(&b, "- [%s] Setup: %s | Punchline: %s\n", str(it, "code"), str(it, "setup"), str(it, "punchline"))
		}
	case "why":
		b.WriteString("## Today's Wonders\n")
		for _, it := range payload.Items {
			fmt.Fprintf(&b, "- [%s] Q: %s\n  Kid-answer: %s\n", str(it, "code"), str(it, "question_text"), str(it, "answer_text"))
			if v := str(it, "wow_fact"); v != "" {
				fmt.Fprintf(&b, "  Wow fact: %s\n", v)
			}
			if v := str(it, "try_at_home"); v != "" {
				fmt.Fprintf(&b, "  Try at home: %s\n", v)
			}
		}
	case "word":
		b.WriteString("## Words of the Day\n")
		for _, it := range payload.Items {
			fmt.Fprintf(&b, "- [%s] %s (%s): %s | Sentence: %s | Phonics: %s\n",
				str(it, "code"), str(it, "word"), str(it, "item_type"), str(it, "meaning_simple"),
				str(it, "example_sentence"), str(it, "phonics_chunks"))
		}
	case "story":
		it := payload.Items[0]
		b.WriteString("## Today's Story Briefing\n")
		fmt.Fprintf(&b, "Story key: %s\nTitle: %s\nMoral: %s\nCharacters: %s\n",
			str(it, "code"), str(it, "title"), str(it, "moral"), str(it, "characters"))
		for i := 1; i <= 6; i++ {
			keys := []string{"beat1_hook", "beat2_setting", "beat3_plot_entry", "beat4_first_half", "beat5_second_half", "beat6_ending"}
			if v := str(it, keys[i-1]); v != "" {
				fmt.Fprintf(&b, "Beat %d: %s\n", i, v)
			}
		}
		fmt.Fprintf(&b, "THE CHOICE: %s (A: %s / B: %s)\n",
			str(it, "choice_question"), str(it, "choice_option_a"), str(it, "choice_option_b"))
		if v := str(it, "sounds"); v != "" {
			fmt.Fprintf(&b, "Sounds: %s\n", v)
		}
		if v := str(it, "kahavat"); v != "" {
			fmt.Fprintf(&b, "Kahavat: %s\n", v)
		}
		if v := str(it, "personalize"); v != "" {
			fmt.Fprintf(&b, "Telling notes: %s\n", v)
		}
		if v := str(it, "safety_notes"); v != "" {
			fmt.Fprintf(&b, "Safety notes: %s\n", v)
		}
	case "spell":
		b.WriteString("## Spell Bee Word List (pick words at the child's current ladder level)\n")
		byLevel := map[int][]string{}
		levels := []int{}
		for _, it := range payload.Items {
			lvl := intOf(it, "level")
			if len(byLevel[lvl]) == 0 {
				levels = append(levels, lvl)
			}
			entry := str(it, "word")
			if v := str(it, "phonics_chunks"); v != "" {
				entry += " (" + v + ")"
			}
			byLevel[lvl] = append(byLevel[lvl], entry)
		}
		sort.Ints(levels)
		for _, lvl := range levels {
			fmt.Fprintf(&b, "Level %d: %s\n", lvl, strings.Join(byLevel[lvl], ", "))
		}
	default:
		return "(no list supplied today - run STARTER MODE)"
	}
	return strings.TrimRight(b.String(), "\n")
}

func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
	return ""
}

func intOf(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

// WriteContentBankState mirrors WriteQuizBankState: the payload also goes to
// memory/state/ so ReadStateFiles re-injects it every turn — a greeting message
// alone does not survive history compaction. The date= header is what
// PruneStaleStateFiles matches, so yesterday's list never leaks into today.
// A nil payload removes any stale file.
func WriteContentBankState(workspace string, payload *ContentPayload, now time.Time) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	if payload == nil || len(payload.Items) == 0 {
		return nil
	}
	name := payload.Bank + "_bank.md"
	if err := os.MkdirAll(stateDir(workspace), 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "CONTENT_BANK: type=%s_bank | date=%s | items=%d\n\n",
		payload.Bank, now.Format("2006-01-02"), len(payload.Items))
	sb.WriteString(contentBlock(payload))
	sb.WriteString("\n")
	return os.WriteFile(filepath.Join(stateDir(workspace), name), []byte(sb.String()), 0o600)
}

// FetchAndWriteContentBank is the main.go seam: fetch, log, write state.
// Never fails the session.
func FetchAndWriteContentBank(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	serviceKey string,
	deviceMac string,
	characterName string,
	workspace string,
) *ContentPayload {
	payload, err := FetchContentBank(ctx, cfg, serviceKey, deviceMac, characterName)
	if err != nil {
		logger.WarnCF("livekit", "Content bank fetch failed; STARTER MODE this session", map[string]any{
			"character":  characterName,
			"device_mac": deviceMac,
			"error":      err.Error(),
		})
		return nil
	}
	if payload == nil {
		return nil
	}
	logger.InfoCF("livekit", "Content bank fetched", map[string]any{
		"character": characterName,
		"bank":      payload.Bank,
		"items":     len(payload.Items),
	})
	if err := WriteContentBankState(workspace, payload, time.Now()); err != nil {
		logger.WarnCF("livekit", "Failed to write content bank state file", map[string]any{
			"device_mac": deviceMac,
			"error":      err.Error(),
		})
	}
	return payload
}
