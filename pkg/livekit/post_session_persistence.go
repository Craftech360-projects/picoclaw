package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultManagerAPIURL = "http://localhost:8002/toy"

var mac12HexPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ai_agent.id is a Postgres uuid; see resolveAgentID for why this is enforced.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (rs *RoomSession) persistPostSessionData(bridge *AgentBridge) {
	if rs == nil || bridge == nil {
		return
	}
	managerPersistenceEnabled := strings.TrimSpace(rs.managerAPIURL) != "" && strings.TrimSpace(rs.deviceMAC) != ""
	if !managerPersistenceEnabled {
		logger.InfoCF("livekit", "Manager post-session persistence disabled; using file-memory mode only", map[string]any{
			"room": rs.roomName(),
		})
	}

	usage := bridge.UsageSnapshot()
	quality := bridge.SessionQualitySnapshot()
	messages := bridge.TranscriptSnapshot()
	if usage.TotalTokens == 0 && len(messages) == 0 {
		logger.DebugCF("livekit", "Skipping post-session persistence: no usage and no transcript", map[string]any{
			"room": rs.roomName(),
		})
		return
	}

	logger.InfoCF("livekit", "Starting post-session persistence", map[string]any{
		"room":       rs.roomName(),
		"device_mac": rs.deviceMAC,
		"messages":   len(messages),
		"tokens":     usage.TotalTokens,
	})
	logger.InfoCF("livekit", "Session quality summary", map[string]any{
		"room":                       rs.roomName(),
		"fallback_count":             quality.FallbackCount,
		"interruption_count":         quality.InterruptionCount,
		"interruption_recovered":     quality.InterruptionRecovered,
		"interruption_recovery_rate": roundTo3(quality.InterruptionRecoveryRate),
		"error_count":                quality.ErrorCount,
		"retry_count":                quality.RetryCount,
		"median_ttft_ms":             quality.MedianTTFTMs,
		"avg_ttft_ms":                quality.AvgTTFTMs,
	})

	if managerPersistenceEnabled {
		usageCtx, usageCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := rs.sendUsageSummary(usageCtx, usage); err != nil {
			logger.WarnCF("livekit", "Failed to persist usage summary", map[string]any{
				"room":   rs.roomName(),
				"error":  err.Error(),
				"tokens": usage.TotalTokens,
			})
		}
		usageCancel()

		// Character progress (the session's final MEMO per type) goes to the DB
		// for every character — the parent app and analytics read it, and the
		// bootstrap restore re-materializes it after the 48h state prune.
		if bridge.agentInstance != nil && strings.TrimSpace(bridge.agentInstance.Workspace) != "" {
			progressCtx, progressCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := rs.sendCharacterProgress(
				progressCtx, bridge.agentInstance.Workspace, bridge.contentBank, messages,
			); err != nil {
				logger.WarnCF("livekit", "Failed to persist character progress", map[string]any{
					"room":  rs.roomName(),
					"error": err.Error(),
				})
			}
			progressCancel()
		}
	}

	// On a last-tap-wins preemption the new session is already waiting on the
	// per-device workspace lock; the ~seconds LLM summary would only delay the
	// handoff and its MEMORY.md write gets clobbered by the new session's
	// restore. Chat history and usage below are still persisted.
	summary := ""
	summaryMessageCount := 0
	if bridge.TeardownPreempted() {
		logger.InfoCF("livekit", "Skipping session summary on preempted handoff", map[string]any{
			"room": rs.roomName(),
		})
	} else {
		summary, summaryMessageCount = rs.finalizeAndPersistSessionSummary(bridge)
	}

	if summary != "" {
		if err := rs.persistSummaryToMemoryFile(bridge, summary, summaryMessageCount); err != nil {
			logger.WarnCF("livekit", "Failed to persist session summary to MEMORY.md", map[string]any{
				"room":  rs.roomName(),
				"error": err.Error(),
			})
		}
	}
	if managerPersistenceEnabled {
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer persistCancel()

		if summary != "" {
			if err := rs.sendSessionSummary(persistCtx, summary, summaryMessageCount); err != nil {
				logger.WarnCF("livekit", "Failed to persist session summary to manager", map[string]any{
					"room":     rs.roomName(),
					"messages": summaryMessageCount,
					"error":    err.Error(),
				})
			}
		}

		if len(messages) > 0 {
			if bridge.RealtimeChatPersistenceEnabled() {
				logger.InfoCF("livekit", "Skipping batch chat-history upload: realtime persistence already enabled", map[string]any{
					"room":     rs.roomName(),
					"messages": len(messages),
				})
			} else if err := rs.sendChatHistory(persistCtx, messages); err != nil {
				logger.WarnCF("livekit", "Failed to persist chat history", map[string]any{
					"room":     rs.roomName(),
					"messages": len(messages),
					"error":    err.Error(),
				})
			}
		}

		if err := rs.sendSessionEnd(persistCtx, len(messages)); err != nil {
			logger.WarnCF("livekit", "Failed to persist session end marker", map[string]any{
				"room":     rs.roomName(),
				"messages": len(messages),
				"error":    err.Error(),
			})
		}
	} else {
		logger.InfoCF("livekit", "Skipping post-session manager chat/session persistence: manager persistence disabled", map[string]any{
			"room":     rs.roomName(),
			"messages": len(messages),
		})
	}

	rs.exportSessionTraceBundle(bridge, usage, quality)
}

func (rs *RoomSession) exportSessionTraceBundle(bridge *AgentBridge, usage UsageSnapshot, quality SessionQualityReport) {
	if rs == nil || bridge == nil || bridge.agentInstance == nil {
		return
	}
	workspace := strings.TrimSpace(bridge.agentInstance.Workspace)
	if workspace == "" {
		return
	}
	traceDir := filepath.Join(workspace, "trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		logger.WarnCF("livekit", "Failed to create trace directory", map[string]any{
			"room":  rs.roomName(),
			"error": err.Error(),
		})
		return
	}

	trace := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"session_id":   rs.roomName(),
		"device_mac":   rs.deviceMAC,
		"usage":        usage,
		"quality":      quality,
		"events":       bridge.SessionTraceSnapshot(),
	}

	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		logger.WarnCF("livekit", "Failed to marshal session trace bundle", map[string]any{
			"room":  rs.roomName(),
			"error": err.Error(),
		})
		return
	}

	tracePath := filepath.Join(traceDir, "session-trace-"+safeTraceSuffix(rs.roomName())+".json")
	if err := os.WriteFile(tracePath, data, 0o600); err != nil {
		logger.WarnCF("livekit", "Failed to write session trace bundle", map[string]any{
			"room":  rs.roomName(),
			"error": err.Error(),
		})
		return
	}

	logger.InfoCF("livekit", "Session trace bundle exported", map[string]any{
		"room":       rs.roomName(),
		"trace_path": tracePath,
		"events":     len(bridge.SessionTraceSnapshot()),
	})
}

func safeTraceSuffix(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	replacer := strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_")
	return replacer.Replace(s)
}

func (rs *RoomSession) sendUsageSummary(ctx context.Context, usage UsageSnapshot) error {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil
	}

	url := strings.TrimRight(rs.managerAPIURL, "/") + "/device/token-usage"
	payload := map[string]any{
		"mac":                          rs.deviceMAC,
		"sessionId":                    rs.roomName(),
		"inputAudioTokens":             usage.InputAudioTokens,
		"inputTextTokens":              usage.InputTextTokens,
		"inputCachedTokens":            usage.InputCachedTokens,
		"inputTokens":                  usage.InputTokens,
		"outputAudioTokens":            usage.OutputAudioTokens,
		"outputTextTokens":             usage.OutputTextTokens,
		"totalTokens":                  usage.TotalTokens,
		"outputTokens":                 usage.OutputTokens,
		"sessionDurationSeconds":       roundTo3(usage.SessionDurationSeconds),
		"avgTtftSeconds":               roundTo3(usage.AvgTTFTSeconds),
		"messageCount":                 usage.MessageCount,
		"totalResponseDurationSeconds": roundTo3(usage.TotalResponseDurationSecond),
	}

	status, body, err := postJSON(ctx, url, payload, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("usage API status=%d body=%s", status, body)
	}

	logger.InfoCF("livekit", "Post-session usage summary persisted", map[string]any{
		"room":        rs.roomName(),
		"device_mac":  rs.deviceMAC,
		"status_code": status,
		"tokens":      usage.TotalTokens,
	})
	return nil
}

func (rs *RoomSession) finalizeAndPersistSessionSummary(bridge *AgentBridge) (string, int) {
	if rs == nil || bridge == nil {
		return "", 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	summary, messageCount, err := bridge.FinalizeSessionSummary(ctx, rs.sessionKeyForParticipant(""))
	if err != nil {
		logger.WarnCF("livekit", "Failed to finalize session summary", map[string]any{
			"room":  rs.roomName(),
			"error": err.Error(),
		})
		return "", messageCount
	}
	if strings.TrimSpace(summary) == "" {
		return "", messageCount
	}

	logger.InfoCF("livekit", "Session summary finalized", map[string]any{
		"room":          rs.roomName(),
		"summary_bytes": len(summary),
		"messages":      messageCount,
	})
	return summary, messageCount
}

func (rs *RoomSession) persistSummaryToMemoryFile(bridge *AgentBridge, summary string, sourceMessageCount int) error {
	if rs == nil || bridge == nil || strings.TrimSpace(summary) == "" {
		return nil
	}
	if bridge.agentInstance == nil {
		return fmt.Errorf("agent instance is nil")
	}
	workspace := strings.TrimSpace(bridge.agentInstance.Workspace)
	if workspace == "" {
		return fmt.Errorf("workspace is empty")
	}

	memoryPath := filepath.Join(workspace, "memory", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		return err
	}

	existing := ""
	if data, err := os.ReadFile(memoryPath); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return err
	}

	var sb strings.Builder
	trimmedExisting := strings.TrimSpace(existing)
	if trimmedExisting == "" {
		sb.WriteString("# Memory\n\n## Session Summaries\n\n")
	} else {
		sb.WriteString(strings.TrimRight(existing, "\n"))
		sb.WriteString("\n")
		if !strings.Contains(strings.ToLower(trimmedExisting), "## session summaries") {
			sb.WriteString("\n## Session Summaries\n")
		}
		sb.WriteString("\n")
	}

	entry := strings.TrimSpace(strings.ReplaceAll(summary, "\n", " "))
	// The file is device-scoped and every character on the device appends to it,
	// so an unlabelled bullet lets Cheeko personalise from Quizzy's session.
	ts := time.Now().Format("2006-01-02 15:04:05 MST")
	if name := strings.TrimSpace(bridge.characterName); name != "" {
		ts += " [" + name + "]"
	}
	if sourceMessageCount > 0 {
		fmt.Fprintf(&sb, "- %s (%d messages): %s\n", ts, sourceMessageCount, entry)
	} else {
		fmt.Fprintf(&sb, "- %s: %s\n", ts, entry)
	}

	const maxMemoryBytes = 64 * 1024
	output := sb.String()
	if len(output) > maxMemoryBytes {
		output = output[len(output)-maxMemoryBytes:]
		header := "# Memory\n\n## Session Summaries\n\n"
		output = header + strings.TrimLeft(output, "\n")
	}
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}

	if err := os.WriteFile(memoryPath, []byte(output), 0o600); err != nil {
		return err
	}
	logger.InfoCF("livekit", "Persisted session summary to MEMORY.md", map[string]any{
		"room":        rs.roomName(),
		"path":        memoryPath,
		"summary_len": len(entry),
	})
	return nil
}

func (rs *RoomSession) sendSessionSummary(ctx context.Context, summary string, sourceMessageCount int) error {
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	if strings.TrimSpace(rs.managerAPISecret) == "" {
		return fmt.Errorf("manager API secret is not configured")
	}

	endpoint := strings.TrimRight(rs.managerAPIURL, "/") +
		"/agent/device/" + url.PathEscape(rs.deviceMAC) +
		"/sessions/" + url.PathEscape(rs.roomName()) + "/summary"
	payload := map[string]any{
		"summary":            summary,
		"sourceMessageCount": sourceMessageCount,
	}
	if rs.agentID != "" {
		payload["agentId"] = rs.agentID
	}
	headers := managerAPIServiceHeaders(rs.managerAPISecret)

	status, body, err := doJSON(ctx, http.MethodPut, endpoint, payload, headers)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("summary API status=%d body=%s", status, body)
	}
	return nil
}

func (rs *RoomSession) sendSessionEnd(ctx context.Context, messageCount int) error {
	if strings.TrimSpace(rs.managerAPISecret) == "" {
		return fmt.Errorf("manager API secret is not configured")
	}

	endpoint := strings.TrimRight(rs.managerAPIURL, "/") +
		"/agent/device/" + url.PathEscape(rs.deviceMAC) +
		"/sessions/" + url.PathEscape(rs.roomName()) + "/end"
	payload := map[string]any{
		"status":       "ended",
		"endedAt":      time.Now().UTC().Format(time.RFC3339Nano),
		"messageCount": messageCount,
	}
	headers := managerAPIServiceHeaders(rs.managerAPISecret)

	status, body, err := doJSON(ctx, http.MethodPost, endpoint, payload, headers)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("session end API status=%d body=%s", status, body)
	}
	return nil
}

func (rs *RoomSession) sendChatHistory(ctx context.Context, messages []PersistedChatMessage) error {
	if len(messages) == 0 {
		return nil
	}
	if strings.TrimSpace(rs.managerAPISecret) == "" {
		return fmt.Errorf("manager API secret is not configured")
	}

	url := strings.TrimRight(rs.managerAPIURL, "/") + "/agent/chat-history/session"
	payload := map[string]any{
		"macAddress":   rs.deviceMAC,
		"sessionId":    rs.roomName(),
		"messages":     messages,
		"messageCount": len(messages),
		"sessionEnd":   time.Now().Unix(),
	}
	if rs.agentID != "" {
		payload["agentId"] = rs.agentID
	}
	headers := managerAPIServiceHeaders(rs.managerAPISecret)

	backoff := time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		status, body, err := postJSON(ctx, url, payload, headers)
		if err == nil && status >= 200 && status < 300 {
			logger.InfoCF("livekit", "Post-session chat history persisted", map[string]any{
				"room":          rs.roomName(),
				"device_mac":    rs.deviceMAC,
				"status_code":   status,
				"message_count": len(messages),
			})
			return nil
		}

		if err == nil && status >= 400 && status < 500 {
			return fmt.Errorf("chat history API status=%d body=%s", status, body)
		}
		if attempt == 3 {
			if err != nil {
				return err
			}
			return fmt.Errorf("chat history API status=%d body=%s", status, body)
		}

		logger.WarnCF("livekit", "Chat history send attempt failed; retrying", map[string]any{
			"room":        rs.roomName(),
			"attempt":     attempt,
			"status_code": status,
		})

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil
}

func managerAPIServiceHeaders(secret string) map[string]string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	return map[string]string{
		"Authorization": "Bearer " + secret,
		"X-Service-Key": secret,
	}
}

func postJSON(ctx context.Context, url string, payload any, headers map[string]string) (int, string, error) {
	return doJSON(ctx, http.MethodPost, url, payload, headers)
}

func doJSON(ctx context.Context, method string, url string, payload any, headers map[string]string) (int, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(body)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.TrimSpace(string(respBody)), nil
}

func roundTo3(value float64) float64 {
	return math.Round(value*1000) / 1000
}

// ResolvePersistenceFields extracts stable persistence identifiers from room info.
// deviceMAC is normalized to aa:bb:cc:dd:ee:ff when present.
func ResolvePersistenceFields(roomName, metadata string) (deviceMAC, agentID string) {
	return resolvePersistenceFields(roomName, metadata)
}

func resolvePersistenceFields(roomName, metadata string) (deviceMAC, agentID string) {
	deviceMAC = resolveDeviceMAC(roomName, metadata)
	agentID = resolveAgentID(metadata)
	return deviceMAC, agentID
}

func resolveDeviceMAC(roomName, metadata string) string {
	md := parseMetadataMap(metadata)
	keys := map[string]struct{}{
		"mac_address": {},
		"device_mac":  {},
		"devicemac":   {},
		"mac":         {},
		"macaddress":  {},
	}
	if mac := normalizeMAC(findFirstString(md, keys)); mac != "" {
		return mac
	}
	return extractMACFromRoomName(roomName)
}

// ResolveKidID returns the child this device is paired to, or "" when the
// gateway sent none. The manager sends the PAIRING, never the child-profile
// fallback: an unpaired device's profile resolves to the owner's most recently
// created child, which is a fine name to greet but the wrong owner for state.
//
// The result becomes a directory component, so it is reduced to digits. A kid id
// is a bigint; anything else is metadata we did not write and is discarded
// rather than sanitised into something plausible.
func ResolveKidID(metadata string) string {
	md := parseMetadataMap(metadata)
	keys := map[string]struct{}{
		"kid_id": {},
		"kidid":  {},
	}
	raw := findFirstScalar(md, keys)
	for _, r := range raw {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return raw
}

// findFirstScalar is findFirstString that also accepts JSON numbers, because an
// id is as likely to arrive unquoted as quoted and a silent miss here would send
// every session back to the MAC-named workspace without any error.
func findFirstScalar(node any, keys map[string]struct{}) string {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if _, ok := keys[lowerKey]; ok {
				switch typed := value.(type) {
				case string:
					if s := strings.TrimSpace(typed); s != "" {
						return s
					}
				case float64:
					if typed == math.Trunc(typed) && typed > 0 {
						return strconv.FormatInt(int64(typed), 10)
					}
				}
			}
			if nested := findFirstScalar(value, keys); nested != "" {
				return nested
			}
		}
	case []any:
		for _, item := range v {
			if nested := findFirstScalar(item, keys); nested != "" {
				return nested
			}
		}
	}
	return ""
}

// resolveAgentID returns the id of the character that is speaking this session.
//
// The gateway spells it character_id and has never sent agent_id — both name the
// same ai_agent.id. Without the second spelling the worker sent no id at all and
// the Manager filled the hole with ai_device.agent_id, the device's DEFAULT
// character, so every character's chat history was filed under that one.
//
// The two spellings are tried in order rather than merged into one key set: map
// iteration is randomised, so a merged lookup would pick a winner at random on
// the metadata that carries both.
//
// character_id must look like a UUID before we believe it. The firmware's hello
// payload uses the SAME key name for a character SLUG ("tara", "masti"), and
// findFirstString recurses into nested objects, so the two namespaces can meet.
// The Manager's agent_id column is @db.Uuid with a foreign key: a slug would
// fail the insert and lose the entire transcript — worse than the
// mis-attribution this resolution exists to fix. agent_id keeps its historical
// permissive behaviour; only the newly-trusted key is checked.
func resolveAgentID(metadata string) string {
	md := parseMetadataMap(metadata)
	if id := strings.TrimSpace(findFirstString(md, map[string]struct{}{"agent_id": {}, "agentid": {}})); id != "" {
		return id
	}
	id := strings.TrimSpace(findFirstString(md, map[string]struct{}{"character_id": {}, "characterid": {}}))
	if uuidPattern.MatchString(id) {
		return id
	}
	return ""
}

func parseMetadataMap(metadata string) map[string]any {
	if strings.TrimSpace(metadata) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(metadata), &out); err != nil {
		return nil
	}
	return out
}

func findFirstString(node any, keys map[string]struct{}) string {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if _, ok := keys[lowerKey]; ok {
				if s, ok := value.(string); ok {
					if strings.TrimSpace(s) != "" {
						return s
					}
				}
			}
			if nested := findFirstString(value, keys); nested != "" {
				return nested
			}
		}
	case []any:
		for _, item := range v {
			if nested := findFirstString(item, keys); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func extractMACFromRoomName(roomName string) string {
	parts := strings.Split(strings.TrimSpace(roomName), "_")
	if len(parts) >= 3 {
		if mac := normalizeMAC(parts[len(parts)-2]); mac != "" {
			return mac
		}
	}
	if len(parts) >= 2 {
		if mac := normalizeMAC(parts[len(parts)-1]); mac != "" {
			return mac
		}
	}
	return ""
}

func normalizeMAC(raw string) string {
	clean := strings.ToLower(strings.TrimSpace(raw))
	clean = strings.ReplaceAll(clean, ":", "")
	clean = strings.ReplaceAll(clean, "-", "")
	if !mac12HexPattern.MatchString(clean) {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s",
		clean[0:2], clean[2:4], clean[4:6], clean[6:8], clean[8:10], clean[10:12])
}
