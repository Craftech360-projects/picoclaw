package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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

type workspaceFileEntry struct {
	Content   string `json:"content"`
	UpdatedAt string `json:"updatedAt"`
}

type workspaceSyncSnapshot struct {
	BaseRevision string              `json:"baseRevision,omitempty"`
	NewRevision  string              `json:"newRevision"`
	Files        []workspaceSyncFile `json:"files,omitempty"`
	Deleted      []string            `json:"deleted,omitempty"`
	Manifest     map[string]any      `json:"manifest,omitempty"`
}

type workspaceSyncFile struct {
	RelativePath string `json:"relativePath"`
	Content      string `json:"content"`
	ContentType  string `json:"contentType,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	SizeBytes    int    `json:"sizeBytes,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type workspaceSyncData struct {
	Revision string              `json:"revision"`
	Manifest map[string]any      `json:"manifest"`
	Files    []workspaceSyncFile `json:"files"`
	Deleted  []string            `json:"deleted"`
	Delta    bool                `json:"delta"`
}

type workspaceManifestFile struct {
	RelativePath string `json:"relativePath"`
	SHA256       string `json:"sha256"`
	SizeBytes    int    `json:"sizeBytes,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type workspaceLocalManifest struct {
	Revision    string                  `json:"revision"`
	GeneratedAt string                  `json:"generatedAt,omitempty"`
	Files       []workspaceManifestFile `json:"files,omitempty"`
	Deleted     []string                `json:"deleted,omitempty"`
}

const workspaceAgentDisplayName = "AGENT.md"
const workspaceSyncManifestPath = ".picoclaw/workspace-manifest.json"
const workspaceSyncOutboxDir = ".picoclaw/sync-outbox"
const workspaceSyncPendingFile = "workspace-upload-pending.json"
const workspaceSyncMaxFileBytes = 256 * 1024
const workspaceSyncListLimit = 2000

var workspaceDiskPaths = map[string]string{
	workspaceAgentDisplayName: "AGENT.md",
	"USER.md":                 "USER.md",
	"SOUL.md":                 "SOUL.md",
	"HEARTBEAT.md":            "HEARTBEAT.md",
	"MEMORY.md":               filepath.Join("memory", "MEMORY.md"),
}

func workspaceDiskMode(diskPath string) os.FileMode {
	if filepath.Clean(diskPath) == filepath.Join("memory", "MEMORY.md") {
		return 0o600
	}
	return 0o644
}

func normalizeWorkspaceRelPath(path string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
}

func isWorkspaceSyncExcluded(relativePath string) bool {
	rel := strings.ToLower(strings.TrimPrefix(normalizeWorkspaceRelPath(relativePath), "./"))
	if rel == "." || rel == "" {
		return true
	}
	if strings.HasPrefix(rel, ".git/") {
		return true
	}
	if strings.HasPrefix(rel, "trace/") {
		return true
	}
	if strings.HasPrefix(rel, "logs/") {
		return true
	}
	if strings.HasPrefix(rel, ".picoclaw/sync-outbox/") {
		return true
	}
	return strings.HasSuffix(rel, ".log")
}

func readLocalWorkspaceManifest(workspaceDir string) workspaceLocalManifest {
	data, err := os.ReadFile(filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncManifestPath)))
	if err != nil {
		return workspaceLocalManifest{}
	}
	var payload workspaceLocalManifest
	if err := json.Unmarshal(data, &payload); err != nil {
		return workspaceLocalManifest{}
	}
	payload.Revision = strings.TrimSpace(payload.Revision)
	return payload
}

func manifestFileMap(files []workspaceManifestFile) map[string]workspaceManifestFile {
	out := make(map[string]workspaceManifestFile, len(files))
	for _, f := range files {
		rel := normalizeWorkspaceRelPath(f.RelativePath)
		if rel == "." || rel == "" {
			continue
		}
		out[rel] = workspaceManifestFile{
			RelativePath: rel,
			SHA256:       strings.TrimSpace(f.SHA256),
			SizeBytes:    f.SizeBytes,
			UpdatedAt:    strings.TrimSpace(f.UpdatedAt),
		}
	}
	return out
}

func manifestFilesFromSync(files []workspaceSyncFile) []workspaceManifestFile {
	out := make([]workspaceManifestFile, 0, len(files))
	for _, f := range files {
		rel := normalizeWorkspaceRelPath(f.RelativePath)
		if rel == "." || rel == "" {
			continue
		}
		out = append(out, workspaceManifestFile{
			RelativePath: rel,
			SHA256:       strings.TrimSpace(f.SHA256),
			SizeBytes:    f.SizeBytes,
			UpdatedAt:    strings.TrimSpace(f.UpdatedAt),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelativePath < out[j].RelativePath
	})
	return out
}

func writeLocalWorkspaceManifest(workspaceDir string, manifest map[string]any, revision string, files []workspaceManifestFile, deleted []string) error {
	if strings.TrimSpace(workspaceDir) == "" {
		return nil
	}
	if manifest == nil {
		manifest = map[string]any{}
	}
	if strings.TrimSpace(revision) != "" {
		manifest["revision"] = strings.TrimSpace(revision)
	}
	if _, ok := manifest["generatedAt"]; !ok {
		manifest["generatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	local := workspaceLocalManifest{
		Revision:    strings.TrimSpace(revision),
		GeneratedAt: fmt.Sprintf("%v", manifest["generatedAt"]),
		Files:       files,
		Deleted:     deleted,
	}
	path := filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncManifestPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(local, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func workspaceSyncPendingPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncOutboxDir), workspaceSyncPendingFile)
}

func markWorkspaceSyncPending(workspaceDir, reason string) {
	if strings.TrimSpace(workspaceDir) == "" {
		return
	}
	path := workspaceSyncPendingPath(workspaceDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	payload := map[string]any{
		"reason":      strings.TrimSpace(reason),
		"updatedAt":   time.Now().UTC().Format(time.RFC3339Nano),
		"source":      "picoclaw-livekit",
		"pendingSync": true,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

func clearWorkspaceSyncPending(workspaceDir string) {
	if strings.TrimSpace(workspaceDir) == "" {
		return
	}
	_ = os.Remove(workspaceSyncPendingPath(workspaceDir))
}

func collectWorkspaceSyncFiles(workspaceDir string) ([]workspaceSyncFile, error) {
	files := make([]workspaceSyncFile, 0, 32)
	err := filepath.WalkDir(workspaceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == workspaceDir {
			return nil
		}
		rel, err := filepath.Rel(workspaceDir, path)
		if err != nil {
			return err
		}
		rel = normalizeWorkspaceRelPath(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if isWorkspaceSyncExcluded(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if isWorkspaceSyncExcluded(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > workspaceSyncMaxFileBytes {
			logger.WarnCF("livekit", "workspace-sync skipped oversized file", map[string]any{
				"path":       rel,
				"size_bytes": info.Size(),
			})
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		files = append(files, workspaceSyncFile{
			RelativePath: rel,
			Content:      string(content),
			ContentType:  "text/plain",
			SHA256:       fmt.Sprintf("%x", sum[:]),
			SizeBytes:    len(content),
			UpdatedAt:    info.ModTime().UTC().Format(time.RFC3339Nano),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, nil
}

func decodeWorkspaceSyncData(body []byte) (workspaceSyncData, error) {
	var wrapper struct {
		Code int               `json:"code"`
		Msg  string            `json:"msg"`
		Data workspaceSyncData `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return workspaceSyncData{}, err
	}
	if wrapper.Code != 0 {
		return workspaceSyncData{}, fmt.Errorf("workspace-sync API code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	return wrapper.Data, nil
}

func tryDownloadWorkspaceSync(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}
	localManifest := readLocalWorkspaceManifest(workspaceDir)
	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-sync" +
		"?limit=" + strconv.Itoa(workspaceSyncListLimit)
	if localManifest.Revision != "" {
		endpoint += "&sinceRevision=" + url.QueryEscape(localManifest.Revision)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("workspace-sync download status=%d", resp.StatusCode)
	}
	data, err := decodeWorkspaceSyncData(body)
	if err != nil {
		return err
	}
	written := 0
	for _, file := range data.Files {
		rel := normalizeWorkspaceRelPath(file.RelativePath)
		if rel == "." || rel == "" || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		target := filepath.Join(workspaceDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			logger.WarnCF("livekit", "workspace-sync: failed to create dir", map[string]any{
				"path":  rel,
				"error": err.Error(),
			})
			continue
		}
		mode := os.FileMode(0o644)
		if rel == "memory/MEMORY.md" || rel == workspaceSyncManifestPath {
			mode = 0o600
		}
		if err := os.WriteFile(target, []byte(file.Content), mode); err != nil {
			logger.WarnCF("livekit", "workspace-sync: failed to write file", map[string]any{
				"path":  rel,
				"error": err.Error(),
			})
			continue
		}
		written++
	}
	for _, deleted := range data.Deleted {
		rel := normalizeWorkspaceRelPath(deleted)
		if rel == "." || rel == "" || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		target := filepath.Join(workspaceDir, filepath.FromSlash(rel))
		_ = os.Remove(target)
	}
	if err := writeLocalWorkspaceManifest(
		workspaceDir,
		data.Manifest,
		data.Revision,
		manifestFilesFromSync(data.Files),
		data.Deleted,
	); err != nil {
		logger.WarnCF("livekit", "workspace-sync: failed to write local manifest", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	}
	logger.InfoCF("livekit", "workspace-sync downloaded from manager", map[string]any{
		"device_mac": deviceMAC,
		"written":    written,
		"revision":   data.Revision,
		"delta":      data.Delta,
	})
	return nil
}

func tryUploadWorkspaceSync(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}
	files, err := collectWorkspaceSyncFiles(workspaceDir)
	if err != nil {
		return err
	}
	localManifest := readLocalWorkspaceManifest(workspaceDir)
	baseRevision := localManifest.Revision
	newRevision := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	localMap := manifestFileMap(localManifest.Files)
	currentMap := make(map[string]workspaceManifestFile, len(files))
	changed := make([]workspaceSyncFile, 0, len(files))
	for _, f := range files {
		rel := normalizeWorkspaceRelPath(f.RelativePath)
		currentMap[rel] = workspaceManifestFile{
			RelativePath: rel,
			SHA256:       strings.TrimSpace(f.SHA256),
			SizeBytes:    f.SizeBytes,
			UpdatedAt:    strings.TrimSpace(f.UpdatedAt),
		}
		prev, ok := localMap[rel]
		if !ok || prev.SHA256 != f.SHA256 {
			changed = append(changed, f)
		}
	}
	deleted := make([]string, 0, len(localMap))
	for rel := range localMap {
		if _, ok := currentMap[rel]; !ok {
			deleted = append(deleted, rel)
		}
	}
	sort.Strings(deleted)
	manifest := map[string]any{
		"source":       "picoclaw-livekit",
		"generatedAt":  time.Now().UTC().Format(time.RFC3339Nano),
		"fileCount":    len(files),
		"changedCount": len(changed),
		"deletedCount": len(deleted),
		"deleted":      deleted,
	}
	payload := workspaceSyncSnapshot{
		BaseRevision: baseRevision,
		NewRevision:  newRevision,
		Files:        changed,
		Deleted:      deleted,
		Manifest:     manifest,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(baseURL, "/") +
		"/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-sync"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("workspace-sync upload conflict: %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("workspace-sync upload status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := writeLocalWorkspaceManifest(
		workspaceDir,
		manifest,
		newRevision,
		manifestFilesFromSync(files),
		deleted,
	); err != nil {
		logger.WarnCF("livekit", "workspace-sync: failed to persist local revision", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	}
	clearWorkspaceSyncPending(workspaceDir)
	logger.InfoCF("livekit", "workspace-sync uploaded to manager", map[string]any{
		"device_mac": deviceMAC,
		"files":      len(changed),
		"deleted":    len(deleted),
		"total":      len(files),
		"revision":   newRevision,
	})
	return nil
}

func downloadWorkspaceFiles(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	if err := tryDownloadWorkspaceSync(ctx, cfg, deviceMAC, workspaceDir); err == nil {
		return nil
	} else {
		logger.WarnCF("livekit", "workspace-sync download failed; falling back to workspace-files", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	}

	return downloadWorkspaceFilesLegacy(ctx, cfg, deviceMAC, workspaceDir)
}

func downloadWorkspaceFilesFastPath(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	// Fast-path intentionally uses the compact legacy workspace-files payload
	// to minimize room startup latency before first greeting.
	return downloadWorkspaceFilesLegacy(ctx, cfg, deviceMAC, workspaceDir)
}

func downloadWorkspaceFilesLegacy(
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
		mode := workspaceDiskMode(diskPath)
		if err := os.WriteFile(target, []byte(entry.Content), mode); err != nil {
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
	if err := tryUploadWorkspaceSync(ctx, cfg, deviceMAC, workspaceDir); err == nil {
		clearWorkspaceSyncPending(workspaceDir)
		return nil
	} else if strings.Contains(strings.ToLower(err.Error()), "conflict") {
		markWorkspaceSyncPending(workspaceDir, err.Error())
		return err
	} else {
		logger.WarnCF("livekit", "workspace-sync upload failed; falling back to workspace-files", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	}

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
		markWorkspaceSyncPending(workspaceDir, fmt.Sprintf("workspace-files upload status=%d", resp.StatusCode))
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
	clearWorkspaceSyncPending(workspaceDir)
	return nil
}
