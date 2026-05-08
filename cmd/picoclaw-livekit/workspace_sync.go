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
	"path"
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
const workspaceSyncDefaultMaxFileBytes = 256 * 1024
const workspaceSyncListLimit = 2000
const workspaceSyncOutboxFilePrefix = "workspace-sync-"

var workspaceDiskPaths = map[string]string{
	workspaceAgentDisplayName: "AGENT.md",
	"USER.md":                 "USER.md",
	"SOUL.md":                 "SOUL.md",
	"HEARTBEAT.md":            "HEARTBEAT.md",
	"MEMORY.md":               filepath.Join("memory", "MEMORY.md"),
}

var protectedWorkspaceCorePaths = map[string]struct{}{
	"agent.md":         {},
	"user.md":          {},
	"soul.md":          {},
	"heartbeat.md":     {},
	"memory/memory.md": {},
}

func workspaceDiskMode(diskPath string) os.FileMode {
	if filepath.Clean(diskPath) == filepath.Join("memory", "MEMORY.md") {
		return 0o600
	}
	return 0o644
}

func isProtectedWorkspaceCoreFile(relativePath string) bool {
	normalized := strings.ToLower(strings.TrimPrefix(normalizeWorkspaceRelPath(relativePath), "./"))
	_, ok := protectedWorkspaceCorePaths[normalized]
	return ok
}

func normalizeWorkspaceRelPath(path string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
}

func isWorkspaceSyncExcluded(relativePath string, cfg *config.LiveKitServiceManagerAPIConfig) bool {
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
	if strings.HasSuffix(rel, ".log") {
		return true
	}
	for _, pattern := range workspaceSyncExcludePatterns(cfg) {
		if workspaceExcludePatternMatches(rel, pattern) {
			return true
		}
	}
	return false
}

func workspaceExcludePatternMatches(rel, pattern string) bool {
	pattern = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(pattern, "\\", "/")))
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}
	if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
		matched, _ := path.Match(pattern, rel)
		return matched
	}
	return rel == pattern
}

func workspaceSyncExcludePatterns(cfg *config.LiveKitServiceManagerAPIConfig) []string {
	defaults := []string{"trace/**", "logs/**", "*.log", ".picoclaw/sync-outbox/**", "skills/**"}
	if cfg == nil || len(cfg.WorkspaceSync.ExcludePatterns) == 0 {
		return defaults
	}
	out := make([]string, 0, len(defaults)+len(cfg.WorkspaceSync.ExcludePatterns))
	out = append(out, defaults...)
	for _, pattern := range cfg.WorkspaceSync.ExcludePatterns {
		if trimmed := strings.TrimSpace(pattern); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func workspaceSyncMaxFileBytes(cfg *config.LiveKitServiceManagerAPIConfig) int {
	if cfg == nil || cfg.WorkspaceSync.MaxFileBytes <= 0 {
		return workspaceSyncDefaultMaxFileBytes
	}
	return cfg.WorkspaceSync.MaxFileBytes
}

func workspaceSyncEnabled(cfg *config.LiveKitServiceManagerAPIConfig) bool {
	if cfg == nil {
		return true
	}
	// default enabled when unset
	if !cfg.WorkspaceSync.Enabled && cfg.WorkspaceSync.IntervalSeconds == 0 && cfg.WorkspaceSync.MaxFileBytes == 0 &&
		cfg.WorkspaceSync.OutboxRetrySecond == 0 && cfg.WorkspaceSync.LockTimeoutSecond == 0 && len(cfg.WorkspaceSync.ExcludePatterns) == 0 {
		return true
	}
	return cfg.WorkspaceSync.Enabled
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

func collectWorkspaceSyncFiles(workspaceDir string, cfg config.LiveKitServiceManagerAPIConfig) ([]workspaceSyncFile, error) {
	maxFileBytes := workspaceSyncMaxFileBytes(&cfg)
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
			if isWorkspaceSyncExcluded(rel, &cfg) {
				return filepath.SkipDir
			}
			return nil
		}
		if isWorkspaceSyncExcluded(rel, &cfg) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(maxFileBytes) {
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
		if isProtectedWorkspaceCoreFile(rel) && strings.TrimSpace(string(content)) == "" {
			logger.WarnCF("livekit", "workspace-sync skipped blank protected core file upload", map[string]any{
				"path": rel,
			})
			return nil
		}
		if bytes.IndexByte(content, 0x00) >= 0 {
			logger.WarnCF("livekit", "workspace-sync skipped file with NUL byte (binary content)", map[string]any{
				"path":       rel,
				"size_bytes": len(content),
			})
			return nil
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
	if !workspaceSyncEnabled(&cfg) {
		return fmt.Errorf("workspace-sync disabled")
	}
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
		if isProtectedWorkspaceCoreFile(rel) && strings.TrimSpace(file.Content) == "" {
			logger.WarnCF("livekit", "workspace-sync skipped blank protected core file restore", map[string]any{
				"path": rel,
			})
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
		if isProtectedWorkspaceCoreFile(rel) {
			logger.WarnCF("livekit", "workspace-sync ignored delete for protected core file", map[string]any{
				"path": rel,
			})
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
	if !workspaceSyncEnabled(&cfg) {
		return fmt.Errorf("workspace-sync disabled")
	}
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}
	files, err := collectWorkspaceSyncFiles(workspaceDir, cfg)
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
	endpoint := strings.TrimRight(baseURL, "/") + "/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-sync"
	respBody, status, err := sendWorkspaceSyncPayload(ctx, endpoint, encoded)
	if err != nil {
		_ = queueWorkspaceSyncOutbox(workspaceDir, encoded, "workspace-sync transport failure")
		return err
	}
	if status == http.StatusConflict {
		logger.WarnCF("livekit", "workspace-sync upload conflict", map[string]any{
			"device_mac":                    deviceMAC,
			"workspace_sync_conflict_count": 1,
			"body":                          strings.TrimSpace(string(respBody)),
		})
		return fmt.Errorf("workspace-sync upload conflict: %s", strings.TrimSpace(string(respBody)))
	}
	if status < 200 || status >= 300 {
		_ = queueWorkspaceSyncOutbox(workspaceDir, encoded, fmt.Sprintf("workspace-sync status=%d", status))
		return fmt.Errorf("workspace-sync upload status=%d body=%s", status, strings.TrimSpace(string(respBody)))
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
		"device_mac":                   deviceMAC,
		"workspace_sync_saved_count":   len(changed),
		"workspace_sync_deleted_count": len(deleted),
		"files":                        len(changed),
		"deleted":                      len(deleted),
		"total":                        len(files),
		"revision":                     newRevision,
	})
	return nil
}

func sendWorkspaceSyncPayload(ctx context.Context, endpoint string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return body, resp.StatusCode, nil
}

func workspaceSyncOutboxPath(workspaceDir string, timestamp time.Time) string {
	name := fmt.Sprintf("%s%d.json", workspaceSyncOutboxFilePrefix, timestamp.UTC().UnixMilli())
	return filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncOutboxDir), name)
}

func queueWorkspaceSyncOutbox(workspaceDir string, payload []byte, reason string) error {
	if strings.TrimSpace(workspaceDir) == "" || len(payload) == 0 {
		return nil
	}
	path := workspaceSyncOutboxPath(workspaceDir, time.Now())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	markWorkspaceSyncPending(workspaceDir, reason)
	return nil
}

func listWorkspaceSyncOutbox(workspaceDir string) ([]string, error) {
	dir := filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncOutboxDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == workspaceSyncPendingFile || !strings.HasPrefix(name, workspaceSyncOutboxFilePrefix) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

func replayWorkspaceSyncOutbox(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) (int, error) {
	if !workspaceSyncEnabled(&cfg) {
		return 0, nil
	}
	baseURL := managerAPIBaseURL(cfg)
	if baseURL == "" || strings.TrimSpace(deviceMAC) == "" || strings.TrimSpace(workspaceDir) == "" {
		return 0, nil
	}
	files, err := listWorkspaceSyncOutbox(workspaceDir)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/agent/device/" + url.PathEscape(deviceMAC) + "/workspace-sync"
	replayed := 0
	for _, file := range files {
		payload, err := os.ReadFile(file)
		if err != nil {
			return replayed, err
		}
		body, status, err := sendWorkspaceSyncPayload(ctx, endpoint, payload)
		if err != nil {
			return replayed, err
		}
		if status == http.StatusConflict {
			// stale outbox payload; discard and continue with newer payloads
			_ = os.Remove(file)
			logger.WarnCF("livekit", "workspace-sync outbox payload conflict discarded", map[string]any{
				"device_mac":                    deviceMAC,
				"file":                          filepath.Base(file),
				"workspace_sync_conflict_count": 1,
			})
			continue
		}
		if status == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(string(body)), "unsupported binary null bytes") {
			// Legacy outbox payload captured before binary/NUL filtering was introduced.
			// Discard it and continue so newer clean snapshots can sync successfully.
			_ = os.Remove(file)
			logger.WarnCF("livekit", "workspace-sync outbox payload discarded due to binary/NUL validation", map[string]any{
				"device_mac": deviceMAC,
				"file":       filepath.Base(file),
			})
			continue
		}
		if status < 200 || status >= 300 {
			return replayed, fmt.Errorf("workspace-sync outbox replay status=%d body=%s", status, strings.TrimSpace(string(body)))
		}
		_ = os.Remove(file)
		replayed++
	}
	remaining, _ := listWorkspaceSyncOutbox(workspaceDir)
	if len(remaining) == 0 {
		clearWorkspaceSyncPending(workspaceDir)
	}
	return replayed, nil
}

func downloadWorkspaceFiles(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	startedAt := time.Now()
	if err := tryDownloadWorkspaceSync(ctx, cfg, deviceMAC, workspaceDir); err == nil {
		logger.InfoCF("livekit", "workspace restore completed", map[string]any{
			"device_mac":                    deviceMAC,
			"workspace_restore_duration_ms": time.Since(startedAt).Milliseconds(),
		})
		return nil
	} else {
		logger.WarnCF("livekit", "workspace-sync download failed; falling back to workspace-files", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	}

	err := downloadWorkspaceFilesLegacy(ctx, cfg, deviceMAC, workspaceDir)
	logger.InfoCF("livekit", "workspace restore completed", map[string]any{
		"device_mac":                    deviceMAC,
		"workspace_restore_duration_ms": time.Since(startedAt).Milliseconds(),
		"fallback":                      true,
	})
	return err
}

func downloadWorkspaceFilesFastPath(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	workspaceDir string,
) error {
	startedAt := time.Now()
	// Fast-path intentionally uses the compact legacy workspace-files payload
	// to minimize room startup latency before first greeting.
	err := downloadWorkspaceFilesLegacy(ctx, cfg, deviceMAC, workspaceDir)
	logger.InfoCF("livekit", "workspace fast-path restore completed", map[string]any{
		"device_mac":                     deviceMAC,
		"workspace_restore_fast_path_ms": time.Since(startedAt).Milliseconds(),
	})
	return err
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
	if replayed, err := replayWorkspaceSyncOutbox(ctx, cfg, deviceMAC, workspaceDir); err != nil {
		logger.WarnCF("livekit", "workspace-sync outbox replay failed before upload", map[string]any{
			"device_mac": deviceMAC,
			"error":      err.Error(),
		})
	} else if replayed > 0 {
		logger.InfoCF("livekit", "workspace-sync outbox replayed", map[string]any{
			"device_mac":                 deviceMAC,
			"replayed_count":             replayed,
			"workspace_sync_outbox_size": len(getWorkspaceOutboxEntries(workspaceDir)),
		})
	}
	if err := tryUploadWorkspaceSync(ctx, cfg, deviceMAC, workspaceDir); err == nil {
		clearWorkspaceSyncPending(workspaceDir)
		return nil
	} else if strings.Contains(strings.ToLower(err.Error()), "conflict") {
		logger.WarnCF("livekit", "workspace-sync conflict detected; attempting single refresh+retry", map[string]any{
			"device_mac": deviceMAC,
		})
		refreshCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if refreshErr := tryDownloadWorkspaceSync(refreshCtx, cfg, deviceMAC, workspaceDir); refreshErr == nil {
			if retryErr := tryUploadWorkspaceSync(ctx, cfg, deviceMAC, workspaceDir); retryErr == nil {
				clearWorkspaceSyncPending(workspaceDir)
				logger.InfoCF("livekit", "workspace-sync conflict resolved by refresh+retry", map[string]any{
					"device_mac": deviceMAC,
				})
				return nil
			} else {
				err = retryErr
			}
		} else {
			logger.WarnCF("livekit", "workspace-sync refresh before retry failed", map[string]any{
				"device_mac": deviceMAC,
				"error":      refreshErr.Error(),
			})
		}
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

func getWorkspaceOutboxEntries(workspaceDir string) []string {
	dir := filepath.Join(workspaceDir, filepath.FromSlash(workspaceSyncOutboxDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out = append(out, entry.Name())
	}
	return out
}
