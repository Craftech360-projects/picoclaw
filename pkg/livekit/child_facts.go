package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// childFact is one lasting fact about the child, as the manager API stores it in
// child_facts. (category, subject) is the de-dup key, so the extraction prompt is
// shown the known facts and told to reuse their keys.
type childFact struct {
	Category  string `json:"category"`
	Subject   string `json:"subject"`
	Fact      string `json:"fact"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// ExtractChildFacts asks the session's model for new or changed facts about the
// child. Reads the final summary as well as the remaining turns: turns already
// folded into the rolling summary are gone from history.
func (ab *AgentBridge) ExtractChildFacts(ctx context.Context, sessionKey string, known []childFact) ([]childFact, error) {
	if ab == nil || ab.sessions == nil || ab.provider == nil {
		return nil, nil
	}
	batch := ab.sessionConversation(sessionKey)
	summary := strings.TrimSpace(ab.sessions.GetSummary(sessionKey))
	if len(batch) == 0 && summary == "" {
		return nil, nil
	}

	prompt := buildChildFactsPrompt(time.Now(), known, summary, batch)
	msg := []providers.Message{{Role: "user", Content: prompt}}
	resp, err := ab.provider.Chat(ctx, msg, nil, ab.modelID, map[string]any{"temperature": 0.2})
	if err != nil {
		return nil, err
	}
	return parseChildFacts(resp.Content)
}

func buildChildFactsPrompt(now time.Time, known []childFact, summary string, batch []providers.Message) string {
	var sb strings.Builder
	sb.WriteString("You keep a short list of lasting facts about a child who talks to a voice toy.\n")
	fmt.Fprintf(&sb, "Today is %s.\n\n", now.Format("2006-01-02"))
	if len(known) > 0 {
		sb.WriteString("Known facts (reuse the same category and subject when a fact is about the same thing):\n")
		for _, f := range known {
			fmt.Fprintf(&sb, "- %s/%s: %s\n", f.Category, f.Subject, f.Fact)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(`From the session below, return ONLY a JSON array of facts to add or update. Each item:
{"category": "family" | "pet" | "likes" | "dislikes" | "school" | "event" | "other",
 "subject": a short key such as "dog", "sister", "favourite_colour",
 "fact": one short English sentence,
 "expires_at": an ISO date, ONLY for time-bound facts such as an upcoming birthday or trip}

Rules:
- Only facts the child said about themselves: family, pets, likes, dislikes, school, upcoming events.
- Write facts in English even if the child spoke Hindi or another language.
- Never store health or medical details, addresses or locations, phone numbers, school names, or other children's surnames.
- Do not store what the toy said, games played, stories told, or quiz answers.
- If a known fact changed, return it with the same category and subject and the new fact.
- Return [] if there is nothing new.
`)
	if summary != "" {
		sb.WriteString("\nSESSION SUMMARY:\n")
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
	if len(batch) > 0 {
		sb.WriteString("\nCONVERSATION:\n")
		for _, m := range batch {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
		}
	}
	return sb.String()
}

// parseChildFacts takes the model's reply, which may wrap the array in prose or
// a code fence, and keeps the items with a subject and a fact. The manager API
// validates everything again; this only has to find the array.
func parseChildFacts(text string) ([]childFact, error) {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start == -1 || end < start {
		return nil, fmt.Errorf("no JSON array in facts reply")
	}
	var raw []childFact
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("decode facts reply: %w", err)
	}
	facts := raw[:0]
	for _, f := range raw {
		if strings.TrimSpace(f.Subject) != "" && strings.TrimSpace(f.Fact) != "" {
			facts = append(facts, f)
		}
	}
	return facts, nil
}

// persistChildFacts runs after the session summary: read the child's known
// facts, extract new ones, send them. Skipped for an unpaired device (the API
// reports no child), so no model call is spent on facts that would be dropped.
func (rs *RoomSession) persistChildFacts(ctx context.Context, bridge *AgentBridge) {
	known, hasChild, err := rs.fetchChildFacts(ctx)
	if err != nil {
		logger.WarnCF("livekit", "Failed to read child facts", map[string]any{"room": rs.roomName(), "error": err.Error()})
		return
	}
	if !hasChild {
		return
	}

	facts, err := bridge.ExtractChildFacts(ctx, rs.sessionKeyForParticipant(""), known)
	if err != nil {
		logger.WarnCF("livekit", "Failed to extract child facts", map[string]any{"room": rs.roomName(), "error": err.Error()})
		return
	}
	if len(facts) == 0 {
		return
	}
	if err := rs.sendChildFacts(ctx, facts); err != nil {
		logger.WarnCF("livekit", "Failed to persist child facts", map[string]any{"room": rs.roomName(), "error": err.Error()})
		return
	}
	logger.InfoCF("livekit", "Child facts persisted", map[string]any{"room": rs.roomName(), "facts": len(facts)})
}

func (rs *RoomSession) childFactsURL(suffix string) string {
	return strings.TrimRight(rs.managerAPIURL, "/") + "/agent/device/" + url.PathEscape(rs.deviceMAC) + suffix
}

func (rs *RoomSession) fetchChildFacts(ctx context.Context) ([]childFact, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rs.childFactsURL("/facts"), nil)
	if err != nil {
		return nil, false, err
	}
	for k, v := range managerAPIServiceHeaders(rs.managerAPISecret) {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("facts API status=%d body=%s", resp.StatusCode, body)
	}
	var envelope struct {
		Data struct {
			KidID *string     `json:"kidId"`
			Facts []childFact `json:"facts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, fmt.Errorf("decode facts: %w", err)
	}
	return envelope.Data.Facts, envelope.Data.KidID != nil, nil
}

func (rs *RoomSession) sendChildFacts(ctx context.Context, facts []childFact) error {
	endpoint := rs.childFactsURL("/sessions/" + url.PathEscape(rs.roomName()) + "/facts")
	status, body, err := doJSON(ctx, http.MethodPut, endpoint, map[string]any{"facts": facts}, managerAPIServiceHeaders(rs.managerAPISecret))
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("facts API status=%d body=%s", status, body)
	}
	return nil
}
