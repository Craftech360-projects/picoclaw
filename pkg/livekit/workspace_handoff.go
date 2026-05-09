package livekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const reconnectHintPath = ".picoclaw/reconnect.hint"

type reconnectHint struct {
	Owner       string    `json:"owner"`
	RequestedAt time.Time `json:"requestedAt"`
}

func reconnectHintFile(workspace string) string {
	return filepath.Join(strings.TrimSpace(workspace), reconnectHintPath)
}

// RecordWorkspaceReconnectHint writes a short-lived reconnect hint used to
// suppress eager workspace deletion while an existing session is still closing.
func RecordWorkspaceReconnectHint(workspace, owner string) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	payload, err := json.MarshalIndent(reconnectHint{
		Owner:       strings.TrimSpace(owner),
		RequestedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	path := reconnectHintFile(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

// ClearWorkspaceReconnectHint removes reconnect hint metadata after a session
// has successfully acquired the workspace lock.
func ClearWorkspaceReconnectHint(workspace string) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	path := reconnectHintFile(workspace)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasRecentWorkspaceReconnectHint reports whether a reconnect hint exists and
// is still fresh enough to justify preserving workspace files on close.
func HasRecentWorkspaceReconnectHint(workspace string, maxAge time.Duration) (bool, string) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return false, ""
	}
	if maxAge <= 0 {
		maxAge = 45 * time.Second
	}

	data, err := os.ReadFile(reconnectHintFile(workspace))
	if err != nil {
		return false, ""
	}
	var hint reconnectHint
	if err := json.Unmarshal(data, &hint); err != nil {
		return false, ""
	}
	if hint.RequestedAt.IsZero() {
		return false, ""
	}
	if time.Since(hint.RequestedAt) > maxAge {
		return false, strings.TrimSpace(hint.Owner)
	}
	return true, strings.TrimSpace(hint.Owner)
}
