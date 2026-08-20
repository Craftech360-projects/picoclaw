package livekit

import (
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

// Character progress in the Manager DB for EVERY character (parent app +
// analytics). The per-turn MEMO already IS the progress record; this file only
// moves it: the session's final MEMO per state type is POSTed at close, and the
// stored current state is restored into memory/state/ at bootstrap — AFTER
// PruneStaleStateFiles, so durable progress (Tikku's ladder, Nani's unfinished
// story) survives the 48h prune that used to erase it.

// StateMemo is one state file's MEMO line, keyed by its type= value.
type StateMemo struct {
	Type string `json:"type"`
	Memo string `json:"memo"`
}

// sessionMarkerFile's mtime records when this session's bootstrap ran. Restored
// files are backdated to their server updated_at, so "modified after the
// marker" means "this session actually played that character" — without it, a
// Cheeko session would re-upload Tikku's restored ladder as Cheeko progress.
const sessionMarkerFile = ".session_marker"

// CollectStateMemos reads the workspace's per-type state files. Only real MEMO
// lines travel: ledgers and the quiz_bank content file are local mechanics, and
// the scored banks' progress already lives in the answer log. Files not touched
// since the session marker (i.e. restored but never played) stay home.
func CollectStateMemos(workspace string) []StateMemo {
	dir := stateDir(workspace)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var markerTime time.Time
	if fi, err := os.Stat(filepath.Join(dir, sessionMarkerFile)); err == nil {
		markerTime = fi.ModTime()
	}
	var memos []StateMemo
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasSuffix(name, stateLedgerSfx) {
			continue
		}
		if !markerTime.IsZero() {
			if fi, err := e.Info(); err == nil && fi.ModTime().Before(markerTime) {
				continue // restored from the DB, not touched this session
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		memo := strings.TrimSpace(string(data))
		if !strings.HasPrefix(strings.ToUpper(memo), "MEMO:") {
			continue // quiz_bank.md and other non-MEMO state
		}
		if t := stateTypeFromMemo(memo); t != "" {
			memos = append(memos, StateMemo{Type: t, Memo: memo})
		}
	}
	return memos
}

// sendCharacterProgress POSTs the session's final MEMO per type, plus the
// content this session was served so the server can mark it as given (the
// no-repeat ledger). Best-effort: progress persistence must never fail a
// session teardown.
//
// Reporting the served codes at CLOSE rather than at serve time is what stops a
// child who connects and immediately drops from burning through the bank: a
// session that never ran reports nothing.
func (rs *RoomSession) sendCharacterProgress(
	ctx context.Context, workspace string, content *ContentPayload, transcript []PersistedChatMessage,
) error {
	memos := CollectStateMemos(workspace)
	if len(memos) == 0 && content == nil {
		return nil
	}
	endpoint := strings.TrimRight(rs.managerAPIURL, "/") + "/progress/session"
	payload := map[string]any{
		"device_mac": rs.deviceMAC,
		"character":  rs.characterName,
		"memos":      memos,
	}
	if content != nil && len(content.Items) > 0 {
		// Only what the transcript shows was actually delivered — a served item
		// the session never reached must stay unheard, not be burned.
		codes := DeliveredCodes(content, transcript)
		if len(codes) > 0 {
			payload["content"] = map[string]any{"bank": content.Bank, "codes": codes}
		}
		logger.InfoCF("livekit", "Content delivery accounted", map[string]any{
			"bank":      content.Bank,
			"served":    len(content.Items),
			"delivered": len(codes),
		})
	}
	status, body, err := postJSON(ctx, endpoint, payload, managerAPIServiceHeaders(rs.managerAPISecret))
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("progress API status=%d body=%s", status, body)
	}
	logger.InfoCF("livekit", "Character progress persisted", map[string]any{
		"room":       rs.roomName(),
		"device_mac": rs.deviceMAC,
		"memo_types": len(memos),
	})
	return nil
}

// RestoreCharacterState pulls the child's stored state and re-materializes any
// state file the prune removed. Local files win: a file already on disk is
// fresher than the last session's upload. Exported for cmd/picoclaw-livekit;
// call AFTER PruneStaleStateFiles. Failures are logged, never fatal — the
// prompts' STARTER MODE covers a missing restore.
func RestoreCharacterState(ctx context.Context, cfg config.LiveKitServiceManagerAPIConfig, serviceKey, deviceMac, workspace string) {
	deviceMac = strings.TrimSpace(deviceMac)
	if deviceMac == "" || strings.TrimSpace(workspace) == "" {
		return
	}
	// Stamp the session marker FIRST (even if the fetch below fails): files
	// older than this at session close were not played this session.
	if err := os.MkdirAll(stateDir(workspace), 0o755); err == nil {
		markerPath := filepath.Join(stateDir(workspace), sessionMarkerFile)
		_ = os.WriteFile(markerPath, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
	}
	endpoint := managerQuizBaseURL(cfg) + "/progress/state?device_mac=" + url.QueryEscape(deviceMac)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	setQuizServiceKey(req, serviceKey)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		logger.DebugCF("livekit", "Character state restore fetch failed", map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.DebugCF("livekit", "Character state restore skipped", map[string]any{"status": resp.StatusCode})
		return
	}
	data, err := unwrapQuizEnvelope(body, "progress state")
	if err != nil {
		return
	}
	var raw struct {
		States []struct {
			StateType string    `json:"state_type"`
			Memo      string    `json:"memo"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"states"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	dir := stateDir(workspace)
	restored := 0
	for _, s := range raw.States {
		t := strings.TrimSpace(strings.ToLower(s.StateType))
		memo := strings.TrimSpace(s.Memo)
		if t == "" || memo == "" || strings.ContainsAny(t, `/\.`) {
			continue
		}
		path := filepath.Join(dir, t+".md")
		if _, err := os.Stat(path); err == nil {
			continue // local file is fresher
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		if err := os.WriteFile(path, []byte(memo+"\n"), 0o600); err == nil {
			// Backdate to the server's timestamp: restored-but-not-played files
			// must sit BEFORE the session marker so they are not re-uploaded.
			when := s.UpdatedAt
			if when.IsZero() {
				when = time.Now().Add(-72 * time.Hour)
			}
			_ = os.Chtimes(path, when, when)
			restored++
		}
	}
	if restored > 0 {
		logger.InfoCF("livekit", "Restored character state from Manager", map[string]any{
			"device_mac": deviceMac,
			"restored":   restored,
		})
	}
}
