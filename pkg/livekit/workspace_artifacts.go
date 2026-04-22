package livekit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const defaultWorkspaceArtifactLimit = 50

// WorkspaceArtifact is a small text file generated inside a device workspace.
type WorkspaceArtifact struct {
	SessionID    string `json:"sessionId,omitempty"`
	RelativePath string `json:"relativePath"`
	Content      string `json:"content,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	SizeBytes    int    `json:"sizeBytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

// WorkspaceArtifactStore mirrors workspace files to a durable source and lists
// them for hydration when a room lands on a different worker.
type WorkspaceArtifactStore interface {
	SaveArtifact(ctx context.Context, artifact WorkspaceArtifact) error
	ListArtifacts(ctx context.Context, limit int) ([]WorkspaceArtifact, error)
}

// ManagerArtifactStore persists artifacts through Manager API.
type ManagerArtifactStore struct {
	baseURL    string
	serviceKey string
	deviceMAC  string
	client     *http.Client
}

type ManagerArtifactStoreConfig struct {
	BaseURL    string
	ServiceKey string
	DeviceMAC  string
	HTTPClient *http.Client
}

func NewManagerArtifactStore(cfg ManagerArtifactStoreConfig) *ManagerArtifactStore {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(cfg.DeviceMAC) == "" {
		return nil
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &ManagerArtifactStore{
		baseURL:    baseURL,
		serviceKey: strings.TrimSpace(cfg.ServiceKey),
		deviceMAC:  strings.TrimSpace(cfg.DeviceMAC),
		client:     client,
	}
}

func (s *ManagerArtifactStore) SaveArtifact(ctx context.Context, artifact WorkspaceArtifact) error {
	if s == nil {
		return nil
	}
	artifact.RelativePath = normalizeArtifactSlashPath(artifact.RelativePath)
	if strings.TrimSpace(artifact.ContentType) == "" {
		artifact.ContentType = "text/plain"
	}
	if strings.TrimSpace(artifact.RelativePath) == "" {
		return nil
	}

	payload := map[string]any{
		"sessionId":    artifact.SessionID,
		"relativePath": artifact.RelativePath,
		"content":      artifact.Content,
		"contentType":  artifact.ContentType,
		"metadata": map[string]any{
			"source": "picoclaw-livekit-tool",
		},
	}
	endpoint := fmt.Sprintf("%s/agent/device/%s/artifacts", s.baseURL, url.PathEscape(s.deviceMAC))
	_, err := s.doManagerJSON(ctx, http.MethodPut, endpoint, payload)
	return err
}

func (s *ManagerArtifactStore) ListArtifacts(ctx context.Context, limit int) ([]WorkspaceArtifact, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultWorkspaceArtifactLimit
	}
	endpoint := fmt.Sprintf(
		"%s/agent/device/%s/artifacts?limit=%d&includeContent=true",
		s.baseURL,
		url.PathEscape(s.deviceMAC),
		limit,
	)
	data, err := s.doManagerJSON(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var artifacts []WorkspaceArtifact
	if len(data) > 0 {
		if err := json.Unmarshal(data, &artifacts); err != nil {
			return nil, fmt.Errorf("decode workspace artifacts: %w", err)
		}
	}
	return artifacts, nil
}

type workspaceArtifactAPIResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (s *ManagerArtifactStore) doManagerJSON(ctx context.Context, method, endpoint string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range managerAPIServiceHeaders(s.serviceKey) {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("manager artifact API status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var wrapper workspaceArtifactAPIResponse
	if err := json.Unmarshal(respBody, &wrapper); err != nil {
		return nil, fmt.Errorf("decode manager artifact response: %w", err)
	}
	if wrapper.Code != 0 {
		return nil, fmt.Errorf("manager artifact API code=%d msg=%s", wrapper.Code, wrapper.Msg)
	}
	return wrapper.Data, nil
}

func artifactRelativePath(workspace, path string) (string, bool) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(path) == "" {
		return "", false
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absWorkspace, candidate)
	}
	absPath, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return normalizeArtifactSlashPath(rel), true
}

func normalizeArtifactSlashPath(path string) string {
	return strings.TrimSpace(filepath.ToSlash(path))
}
