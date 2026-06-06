package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/session"
)

func buildManagerSessionStore(
	lkCfg config.LiveKitServiceConfig,
	deviceMAC string,
	agentID string,
	sessionID string,
) session.SessionStore {
	if !managerSessionStoreEnabled(lkCfg.ManagerAPI) {
		return nil
	}
	if strings.TrimSpace(deviceMAC) == "" {
		logger.WarnCF("livekit", "Manager-backed session store disabled for room: device MAC unavailable", map[string]any{
			"session_id": sessionID,
		})
		return nil
	}
	if strings.TrimSpace(sessionID) == "" {
		logger.WarnCF("livekit", "Manager-backed session store disabled for room: session ID unavailable", map[string]any{
			"device_mac": deviceMAC,
		})
		return nil
	}

	baseURL := managerAPIBaseURL(lkCfg.ManagerAPI)
	serviceKey := managerAPIServiceKey()
	if serviceKey == "" {
		logger.WarnCF("livekit", "Manager-backed session store enabled without service key; bootstrap may fail", map[string]any{
			"session_id": sessionID,
			"device_mac": deviceMAC,
		})
	}

	return session.NewManagerAPIBackend(session.ManagerAPIBackendConfig{
		BaseURL:         baseURL,
		ServiceKey:      serviceKey,
		MACAddress:      deviceMAC,
		AgentID:         agentID,
		SessionID:       sessionID,
		RecentLimit:     lkCfg.ManagerAPI.RecentLimit,
		HistoryPageSize: lkCfg.ManagerAPI.WorkspaceRestore.HistoryPageSize,
		MaxHistoryPages: lkCfg.ManagerAPI.WorkspaceRestore.MaxHistoryPagesOnIdle,
	})
}

func managerSessionStoreEnabled(cfg config.LiveKitServiceManagerAPIConfig) bool {
	if cfg.SessionStoreEnabled {
		return true
	}
	raw := strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED"))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

func managerAPIBaseURL(cfg config.LiveKitServiceManagerAPIConfig) string {
	for _, value := range []string{
		os.Getenv("MANAGER_API_URL"),
		cfg.BaseURL,
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func managerAPIServiceKey() string {
	for _, key := range []string{
		"PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY",
		"SERVICE_SECRET_KEY",
		"MANAGER_API_SECRET",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
