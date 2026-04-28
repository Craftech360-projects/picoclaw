package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type workspaceFileEntry struct {
	Content   string `json:"content"`
	UpdatedAt string `json:"updatedAt"`
}

var workspaceDiskPaths = map[string]string{
	"IDENTITY.md":  "IDENTITY.md",
	"USER.md":      "USER.md",
	"SOUL.md":      "SOUL.md",
	"HEARTBEAT.md": "HEARTBEAT.md",
	"MEMORY.md":    filepath.Join("memory", "MEMORY.md"),
}

func downloadWorkspaceFiles(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}

	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-files"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("workspace-files download status=%d", resp.StatusCode)
	}

	var wrapper struct {
		Code int                           `json:"code"`
		Msg  string                        `json:"msg"`
		Data map[string]workspaceFileEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return fmt.Errorf("decode workspace-files response: %w", err)
	}
	if wrapper.Code != 0 {
		return fmt.Errorf("workspace-files API code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}

	written := 0
	for displayName, diskPath := range workspaceDiskPaths {
		entry, ok := wrapper.Data[displayName]
		if !ok || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		target := filepath.Join(workspaceDir, diskPath)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			logger.WarnCF("livekit", "workspace-files: failed to create dir", map[string]any{
				"path":  diskPath,
				"error": err.Error(),
			})
			continue
		}
		if err := os.WriteFile(target, []byte(entry.Content), 0o644); err != nil {
			logger.WarnCF("livekit", "workspace-files: failed to write file", map[string]any{
				"path":  diskPath,
				"error": err.Error(),
			})
			continue
		}
		written++
	}

	logger.InfoCF("livekit", "workspace-files downloaded from manager", map[string]any{
		"device_mac": deviceMAC,
		"written":    written,
	})
	return nil
}

func uploadWorkspaceFiles(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}

	payload := make(map[string]string, len(workspaceDiskPaths))
	for displayName, diskPath := range workspaceDiskPaths {
		target := filepath.Join(workspaceDir, diskPath)
		data, err := os.ReadFile(target)
		if err != nil {
			payload[displayName] = ""
			continue
		}
		payload[displayName] = string(data)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-files"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf(
			"workspace-files upload status=%d body=%s",
			resp.StatusCode,
			strings.TrimSpace(string(respBody)),
		)
	}

	logger.InfoCF("livekit", "workspace-files uploaded to manager", map[string]any{
		"device_mac": deviceMAC,
		"files":      len(payload),
	})
	return nil
}
