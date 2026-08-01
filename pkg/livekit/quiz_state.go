package livekit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Quiz state lives in a runtime-managed section of memory/MEMORY.md so it (a)
// is injected into the LLM context by pkg/agent/context.go without tool calls,
// (b) round-trips through manager workspace sync, and (c) survives voice-context
// summarization, which only trims in-session chat history. The persona prompt
// promises "the runtime saves this line locally"; this file is that runtime.
const (
	quizStateHeading     = "## Quiz State"
	quizStateStartMarker = "<!-- quiz-state:start -->"
	quizStateEndMarker   = "<!-- quiz-state:end -->"
	// ponytail: tz-blind 48h bound; per-device timezone if parents complain
	quizStateMaxAge = 48 * time.Hour
)

var quizMemoDateRE = regexp.MustCompile(`(?i)\bdate\s*=\s*(\d{4}-\d{2}-\d{2})`)

// extractQuizMemoLine returns the last line-anchored "MEMO:" line of an
// assistant reply, with expression tags stripped. "" when absent.
func extractQuizMemoLine(content string) string {
	last := ""
	// Tags are stripped per line, after splitting: the global tag regex eats
	// surrounding whitespace including the newline before "[neutral] MEMO:",
	// which would destroy the line anchor.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(voiceExpressionTagRE.ReplaceAllString(line, " "))
		if len(trimmed) < 5 || !strings.EqualFold(trimmed[:5], "memo:") {
			continue
		}
		body := strings.TrimSpace(trimmed[5:])
		if body == "" {
			continue
		}
		last = "MEMO: " + body
	}
	return last
}

// upsertQuizStateSection replaces (or appends) the marked section with a single
// memoLine. Any damaged remnant of a previous section (64KB tail-truncation in
// persistSummaryToMemoryFile, hand edits) is removed first so exactly one
// section exists afterwards.
func upsertQuizStateSection(memoryPath, memoLine string) error {
	memoLine = strings.TrimSpace(memoLine)
	if memoLine == "" {
		return nil
	}
	existing := ""
	if data, err := os.ReadFile(memoryPath); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		return err
	}

	body := removeQuizStateSection(existing)
	section := fmt.Sprintf("%s\n\n%s\n%s\n%s\n", quizStateHeading, quizStateStartMarker, memoLine, quizStateEndMarker)
	var out string
	if strings.TrimSpace(body) == "" {
		out = "# Memory\n\n" + section
	} else {
		out = strings.TrimRight(body, "\n") + "\n\n" + section
	}
	return os.WriteFile(memoryPath, []byte(out), 0o600)
}

// removeQuizStateSection strips the heading, the markers, and everything
// between the markers — tolerating a missing start OR end marker so a
// truncation-decapitated section still gets cleaned up.
func removeQuizStateSection(s string) string {
	if start := strings.Index(s, quizStateStartMarker); start >= 0 {
		if end := strings.Index(s[start:], quizStateEndMarker); end >= 0 {
			s = s[:start] + s[start+end+len(quizStateEndMarker):]
		} else {
			// start marker but no end: the section is the tail, drop it
			s = s[:start]
		}
	} else if end := strings.Index(s, quizStateEndMarker); end >= 0 {
		// end marker but no start: drop any orphaned MEMO line before it
		head := s[:end]
		if memoAt := strings.LastIndex(head, "MEMO:"); memoAt >= 0 {
			if nl := strings.LastIndex(head[:memoAt], "\n"); nl >= 0 {
				head = head[:nl+1]
			} else {
				head = ""
			}
		}
		s = head + s[end+len(quizStateEndMarker):]
	}
	s = strings.ReplaceAll(s, quizStateHeading+"\n", "")
	s = strings.ReplaceAll(s, quizStateHeading, "")
	return s
}

// PruneStaleQuizState removes the section when its date= is older than 48h.
// Called once per session bootstrap, after workspace hydration. Exported for
// cmd/picoclaw-livekit. Unparseable or absent dates are kept (fail-open): the
// persona prompt's own date check still governs same-day/new-day behavior.
func PruneStaleQuizState(memoryPath string, now time.Time) (bool, error) {
	data, err := os.ReadFile(memoryPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := string(data)
	start := strings.Index(s, quizStateStartMarker)
	if start < 0 {
		return false, nil
	}
	m := quizMemoDateRE.FindStringSubmatch(s[start:])
	if m == nil {
		return false, nil
	}
	d, perr := time.Parse("2006-01-02", m[1])
	if perr != nil {
		return false, nil
	}
	if now.Sub(d) <= quizStateMaxAge {
		return false, nil
	}
	if err := os.WriteFile(memoryPath, []byte(removeQuizStateSection(s)), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// maybePersistQuizState is the per-turn hook: extract the MEMO from a finished
// assistant reply and upsert it. Never fails the turn.
func maybePersistQuizState(workspace, assistantContent string) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return
	}
	memo := extractQuizMemoLine(assistantContent)
	if memo == "" {
		return
	}
	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	if err := upsertQuizStateSection(memoryPath, memo); err != nil {
		logger.WarnCF("livekit", "Failed to persist quiz MEMO to MEMORY.md", map[string]any{
			"path":  memoryPath,
			"error": err.Error(),
		})
		return
	}
	logger.InfoCF("livekit", "Persisted quiz MEMO to MEMORY.md", map[string]any{
		"path":     memoryPath,
		"memo_len": len(memo),
	})
}
