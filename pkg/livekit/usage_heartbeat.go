package livekit

// Mid-session usage heartbeat (SUB-5): every 5 minutes the session's
// cumulative usage is POSTed to manager-api, so a crashed worker loses at
// most 5 minutes of billable time and the daily minute cap can end a running
// session. On {cutoff:true} the session finishes through the same graceful
// farewell path the gateway's end_prompt uses — the child hears a goodbye,
// not a drop.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultUsageHeartbeatInterval = 5 * time.Minute

const cutoffFarewellPrompt = "It is time for a little break now! We had so much fun talking today. See you soon!"

func usageHeartbeatInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("USAGE_HEARTBEAT_INTERVAL"))
	if raw == "" {
		return defaultUsageHeartbeatInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.WarnCF("livekit", "Invalid USAGE_HEARTBEAT_INTERVAL; using default", map[string]any{
			"value":   raw,
			"default": defaultUsageHeartbeatInterval.String(),
		})
		return defaultUsageHeartbeatInterval
	}
	return d
}

// startUsageHeartbeat launches the heartbeat loop for this session. Skipped in
// file-memory-only mode (no manager URL / MAC), same as post-session persistence.
func (rs *RoomSession) startUsageHeartbeat() {
	if strings.TrimSpace(rs.managerAPIURL) == "" || strings.TrimSpace(rs.deviceMAC) == "" {
		return
	}
	interval := usageHeartbeatInterval()
	logger.InfoCF("livekit", "Usage heartbeat started", map[string]any{
		"room":       rs.roomName(),
		"device_mac": rs.deviceMAC,
		"interval":   interval.String(),
	})
	go rs.usageHeartbeatLoop(rs.ctx, interval, func() {
		go rs.handleEndPrompt(cutoffFarewellPrompt)
	})
}

// usageHeartbeatLoop beats until the session context ends. A failed POST is
// warn-only (billing catches up next beat or at session end); cutoff fires
// onCutoff once and stops the loop — the final usage flush happens in Leave.
func (rs *RoomSession) usageHeartbeatLoop(ctx context.Context, interval time.Duration, onCutoff func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff, err := rs.sendUsageHeartbeat(ctx)
			if err != nil {
				logger.WarnCF("livekit", "Usage heartbeat failed", map[string]any{
					"room":  rs.roomName(),
					"error": err.Error(),
				})
				continue
			}
			if cutoff {
				logger.InfoCF("livekit", "Daily minute cap breached — ending session gracefully", map[string]any{
					"room":       rs.roomName(),
					"device_mac": rs.deviceMAC,
				})
				onCutoff()
				return
			}
		}
	}
}

// sendUsageHeartbeat POSTs the cumulative usage so far and reports whether
// manager-api wants the session cut. Any failure answers "no cutoff" — the
// cap is an abuse backstop, and a metering hiccup must never end a session.
func (rs *RoomSession) sendUsageHeartbeat(ctx context.Context) (bool, error) {
	rs.mu.Lock()
	bridge := rs.bridge
	rs.mu.Unlock()
	if bridge == nil {
		return false, nil // session already tearing down
	}
	usage := bridge.UsageSnapshot()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(rs.managerAPIURL, "/") +
		"/device/" + url.PathEscape(rs.deviceMAC) + "/usage-heartbeat"
	payload := map[string]any{
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

	status, body, err := postJSON(ctx, endpoint, payload, managerAPIServiceHeaders(rs.managerAPISecret))
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("heartbeat API status=%d body=%s", status, body)
	}

	var envelope struct {
		Data struct {
			Cutoff bool `json:"cutoff"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return false, fmt.Errorf("parse heartbeat response: %w", err)
	}
	return envelope.Data.Cutoff, nil
}
