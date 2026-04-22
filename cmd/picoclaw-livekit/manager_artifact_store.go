package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	picokit "github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func buildManagerArtifactStore(
	lkCfg config.LiveKitServiceConfig,
	deviceMAC string,
) picokit.WorkspaceArtifactStore {
	if !managerSessionStoreEnabled(lkCfg.ManagerAPI) {
		return nil
	}
	if strings.TrimSpace(deviceMAC) == "" {
		return nil
	}
	return picokit.NewManagerArtifactStore(picokit.ManagerArtifactStoreConfig{
		BaseURL:    managerAPIBaseURL(lkCfg.ManagerAPI),
		ServiceKey: managerAPIServiceKey(),
		DeviceMAC:  deviceMAC,
	})
}

func hydrateWorkspaceArtifacts(ctx context.Context, store picokit.WorkspaceArtifactStore, workspace string, limit int) (int, error) {
	if store == nil || strings.TrimSpace(workspace) == "" {
		return 0, nil
	}
	hydrateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	artifacts, err := store.ListArtifacts(hydrateCtx, limit)
	if err != nil {
		return 0, err
	}

	written := 0
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.ContentType) != "" && artifact.ContentType != "text/plain" {
			continue
		}
		target, ok := safeWorkspaceArtifactPath(workspace, artifact.RelativePath)
		if !ok {
			logger.WarnCF("livekit", "Skipping unsafe workspace artifact path during hydration", map[string]any{
				"path": artifact.RelativePath,
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return written, err
		}
		if err := os.WriteFile(target, []byte(artifact.Content), 0644); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

func safeWorkspaceArtifactPath(workspace, relativePath string) (string, bool) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(relativePath) == "" {
		return "", false
	}
	normalized := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relativePath)))
	if filepath.IsAbs(normalized) || normalized == "." || !filepath.IsLocal(normalized) {
		return "", false
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	target, err := filepath.Abs(filepath.Join(absWorkspace, normalized))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absWorkspace, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return target, true
}
