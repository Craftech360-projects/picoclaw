package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultManagerAPIURL = "http://localhost:8002/toy"

var mac12HexPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

func (rs *RoomSession) persistPostSessionData(bridge *AgentBridge) {
	if rs == nil || bridge == nil {
		return
	}
	if strings.TrimSpace(rs.managerAPIURL) == "" {
		logger.DebugCF("livekit", "Skipping post-session persistence: manager API URL not configured", map[string]any{
			"room": rs.roomName(),
		})
		return
	}
	if rs.deviceMAC == "" {
		logger.WarnCF("livekit", "Skipping post-session persistence: device MAC unavailable", map[string]any{
			"room": rs.roomName(),
		})
		return
	}

	usage := bridge.UsageSnapshot()
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

	usageCtx, usageCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := rs.sendUsageSummary(usageCtx, usage); err != nil {
		logger.WarnCF("livekit", "Failed to persist usage summary", map[string]any{
			"room":   rs.roomName(),
			"error":  err.Error(),
			"tokens": usage.TotalTokens,
		})
	}
	usageCancel()

	summary, summaryMessageCount := rs.finalizeAndPersistSessionSummary(bridge)

	if summary != "" {
		summaryCtx, summaryCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := rs.sendSessionSummary(summaryCtx, summary, summaryMessageCount); err != nil {
			logger.WarnCF("livekit", "Failed to persist session summary", map[string]any{
				"room":  rs.roomName(),
				"error": err.Error(),
			})
		}
		summaryCancel()
	}

	if bridge.RealtimeChatPersistenceEnabled() {
		logger.InfoCF("livekit", "Skipping post-session chat history: real-time manager persistence enabled", map[string]any{
			"room":     rs.roomName(),
			"messages": len(messages),
		})
	} else {
		chatCtx, chatCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := rs.sendChatHistory(chatCtx, messages); err != nil {
			logger.WarnCF("livekit", "Failed to persist chat history", map[string]any{
				"room":     rs.roomName(),
				"error":    err.Error(),
				"messages": len(messages),
			})
		}
		chatCancel()
	}

	endCtx, endCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := rs.sendSessionEnd(endCtx, len(messages)); err != nil {
		logger.WarnCF("livekit", "Failed to mark manager session ended", map[string]any{
			"room":  rs.roomName(),
			"error": err.Error(),
		})
	}
	endCancel()
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

func resolveAgentID(metadata string) string {
	md := parseMetadataMap(metadata)
	keys := map[string]struct{}{
		"agent_id": {},
		"agentid":  {},
	}
	return strings.TrimSpace(findFirstString(md, keys))
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
