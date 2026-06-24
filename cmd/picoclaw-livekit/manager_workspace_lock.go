package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// errWorkspaceLockPreempted is returned by heartbeat/renew when the Manager
// reports this holder was fenced out by a newer session (HTTP 409 +
// lockErrorCode=LOCK_PREEMPTED). It is distinguishable from transient/network
// errors so the worker can branch to the preempted-close path (skip workspace
// upload, keep chat history, do not release the lock).
var errWorkspaceLockPreempted = errors.New("manager workspace lock preempted by newer holder")

// Heartbeat cadence and lease lifetime for the distributed (Manager) lock.
//
// last-tap-wins preemption needs the OLD holder to notice it was fenced out
// within ~1-2s of the new session's preemptive acquire, so we heartbeat every
// 1.5s (was 5s). The Manager lease TTL is lowered toward ~8s: short enough that
// a crashed holder's lease lapses quickly (so a non-preempt acquire can reclaim
// a dead lock without a long wait), but still >4x the heartbeat interval so a
// single dropped heartbeat does not self-evict a healthy holder.
const managerWorkspaceLockHeartbeatInterval = 1500 * time.Millisecond
const managerWorkspaceLockLeaseTTLSeconds = 8

type managerWorkspaceLockState struct {
	DeviceMAC    string `json:"deviceMac"`
	HolderID     string `json:"holderId"`
	FencingToken int64  `json:"fencingToken"`
}

type managerWorkspaceLockAcquireResult struct {
	Acquired bool                       `json:"acquired"`
	Lock     *managerWorkspaceLockState `json:"lock"`
	Current  *managerWorkspaceLockState `json:"current"`
}

type managerWorkspaceLockLease struct {
	cfg          config.LiveKitServiceManagerAPIConfig
	deviceMAC    string
	holderID     string
	fencingToken int64
	leaseTTL     int
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once

	// preempted is set once the Manager fences this holder out (a newer session
	// took over). onPreempted (if set) is fired exactly once to trigger room
	// teardown via the preempted-close path.
	preempted     atomic.Bool
	onPreemptedMu sync.Mutex
	onPreempted   func()
	preemptFired  atomic.Bool
}

// WasPreempted reports whether this lease was fenced out by a newer session.
// The bridge close callbacks use it to decide whether to skip the workspace
// upload and skip releasing the lock (the new room owns it now).
func (l *managerWorkspaceLockLease) WasPreempted() bool {
	if l == nil {
		return false
	}
	return l.preempted.Load()
}

// SetOnPreempted registers a callback fired once when this lease is fenced out.
// It is wired to trigger a graceful room teardown so the old session stops
// promptly instead of running to a natural end.
func (l *managerWorkspaceLockLease) SetOnPreempted(fn func()) {
	if l == nil {
		return
	}
	l.onPreemptedMu.Lock()
	l.onPreempted = fn
	l.onPreemptedMu.Unlock()
}

func (l *managerWorkspaceLockLease) markPreempted() {
	if l == nil {
		return
	}
	l.preempted.Store(true)
	if l.preemptFired.CompareAndSwap(false, true) {
		l.onPreemptedMu.Lock()
		fn := l.onPreempted
		l.onPreemptedMu.Unlock()
		if fn != nil {
			go fn()
		}
	}
}

func (l *managerWorkspaceLockLease) startHeartbeat(interval time.Duration) {
	if l == nil {
		return
	}
	if interval <= 0 {
		interval = managerWorkspaceLockHeartbeatInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(l.doneCh)
		for {
			select {
			case <-l.stopCh:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				_, err := heartbeatManagerWorkspaceLock(ctx, l.cfg, l.deviceMAC, l.holderID, l.fencingToken, l.leaseTTL)
				cancel()
				if err != nil {
					if errors.Is(err, errWorkspaceLockPreempted) {
						// A newer session fenced us out. Mark preempted (so the
						// close path skips upload + lock release, keeps chat
						// history) and trigger room teardown. Stop heartbeating.
						logger.InfoCF("livekit", "Manager workspace lock preempted by newer session; tearing down", map[string]any{
							"device_mac":    l.deviceMAC,
							"holder_id":     l.holderID,
							"fencing_token": l.fencingToken,
						})
						l.markPreempted()
						return
					}
					logger.WarnCF("livekit", "Manager workspace lock heartbeat failed", map[string]any{
						"device_mac":    l.deviceMAC,
						"holder_id":     l.holderID,
						"fencing_token": l.fencingToken,
						"error":         err.Error(),
					})
				}
			}
		}
	}()
}

func (l *managerWorkspaceLockLease) Release(reason string) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.stopCh)
		<-l.doneCh
		if l.preempted.Load() {
			// We were fenced out: the new room owns the lock now. Do NOT call
			// release — a stale-token release is a no-op on the Manager anyway,
			// but skipping it avoids any chance of disturbing the new holder.
			logger.InfoCF("livekit", "Skipping manager workspace lock release (preempted)", map[string]any{
				"device_mac":    l.deviceMAC,
				"holder_id":     l.holderID,
				"fencing_token": l.fencingToken,
				"reason":        reason,
			})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		released, err := releaseManagerWorkspaceLock(ctx, l.cfg, l.deviceMAC, l.holderID, l.fencingToken)
		if err != nil {
			logger.WarnCF("livekit", "Failed to release manager workspace lock", map[string]any{
				"device_mac":    l.deviceMAC,
				"holder_id":     l.holderID,
				"fencing_token": l.fencingToken,
				"reason":        reason,
				"error":         err.Error(),
			})
			return
		}
		logger.InfoCF("livekit", "Released manager workspace lock", map[string]any{
			"device_mac":    l.deviceMAC,
			"holder_id":     l.holderID,
			"fencing_token": l.fencingToken,
			"reason":        reason,
			"released":      released,
		})
	})
}

func acquireManagerWorkspaceLockWithRetry(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	holderID string,
	waitTimeout time.Duration,
	leaseTTLSeconds int,
) (*managerWorkspaceLockLease, error) {
	baseURL := managerAPIBaseURL(cfg)
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(deviceMAC) == "" {
		return nil, nil
	}
	serviceKey := strings.TrimSpace(managerAPIServiceKey())
	if serviceKey == "" {
		return nil, fmt.Errorf("manager API service key is required for distributed workspace lock")
	}
	if waitTimeout <= 0 {
		waitTimeout = 30 * time.Second
	}

	// For last-tap-wins, the distributed lease TTL is intentionally short
	// (~8s) and decoupled from the caller's lock_timeout (which governs the
	// LOCAL file-lock acquire wait). A short TTL lets a crashed holder's lease
	// lapse quickly so a future non-preempt acquire can reclaim a dead lock,
	// while preemption itself does not depend on the TTL at all. We keep the
	// param for signature stability but override it to the tuned constant
	// unless the caller explicitly requested something smaller.
	if leaseTTLSeconds <= 0 || leaseTTLSeconds > managerWorkspaceLockLeaseTTLSeconds {
		leaseTTLSeconds = managerWorkspaceLockLeaseTTLSeconds
	}
	leaseTTLSeconds = clampPositiveInt(leaseTTLSeconds, 6, 120)

	// last-tap-wins: a just-dispatched session is by definition the newest, so we
	// FORCE-acquire (preempt=true). If the device lock is held by a DIFFERENT
	// holder the Manager bumps the fencing_token and hands it to us immediately —
	// no 30s wait. If two new dispatches race, the fencing_token serializes them.
	result, err := acquireManagerWorkspaceLock(ctx, cfg, deviceMAC, holderID, leaseTTLSeconds, 2, true)
	if err != nil {
		return nil, err
	}
	if result.Acquired && result.Lock != nil {
		lease := &managerWorkspaceLockLease{
			cfg:          cfg,
			deviceMAC:    deviceMAC,
			holderID:     holderID,
			fencingToken: result.Lock.FencingToken,
			leaseTTL:     leaseTTLSeconds,
			stopCh:       make(chan struct{}),
			doneCh:       make(chan struct{}),
		}
		lease.startHeartbeat(managerWorkspaceLockHeartbeatInterval)
		return lease, nil
	}

	// A preemptive acquire should always succeed against a different holder. The
	// only non-acquire outcome is a concurrent racing dispatch that got there
	// first (and fenced us) — treat as busy without a long wait.
	currentOwner := ""
	if result.Current != nil {
		currentOwner = result.Current.HolderID
	}
	if currentOwner != "" {
		return nil, fmt.Errorf("manager workspace lock busy (owner=%s)", currentOwner)
	}
	return nil, fmt.Errorf("manager workspace lock busy")
}

func acquireManagerWorkspaceLock(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	holderID string,
	leaseTTLSeconds int,
	staleGraceSeconds int,
	preempt bool,
) (managerWorkspaceLockAcquireResult, error) {
	var out managerWorkspaceLockAcquireResult
	baseURL := managerAPIBaseURL(cfg)
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(deviceMAC) == "" {
		return out, nil
	}
	payload := map[string]any{
		"holderId":          holderID,
		"leaseTTLSeconds":   leaseTTLSeconds,
		"staleGraceSeconds": staleGraceSeconds,
		"preempt":           preempt,
	}
	body, _, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "acquire", payload)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func heartbeatManagerWorkspaceLock(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	holderID string,
	fencingToken int64,
	leaseTTLSeconds int,
) (bool, error) {
	payload := map[string]any{
		"holderId":        holderID,
		"fencingToken":    fencingToken,
		"leaseTTLSeconds": leaseTTLSeconds,
	}
	body, status, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "heartbeat", payload)
	if status == http.StatusConflict {
		// 409 means the heartbeat was rejected. Inspect the machine-readable
		// lockErrorCode to tell "I was preempted" apart from a transient miss.
		if workspaceLockResponseIsPreempted(body, err) {
			return false, errWorkspaceLockPreempted
		}
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("workspace-lock heartbeat rejected (409)")
	}
	if err != nil {
		return false, err
	}
	var decoded struct {
		Renewed bool `json:"renewed"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false, err
	}
	return decoded.Renewed, nil
}

// workspaceLockResponseIsPreempted reports whether a 409 heartbeat response (or
// its error text) indicates LOCK_PREEMPTED. The Manager returns the code both in
// the JSON body's data.lockErrorCode and, for non-2xx, in the raw error string.
func workspaceLockResponseIsPreempted(body []byte, rawErr error) bool {
	if len(body) > 0 {
		var decoded struct {
			LockErrorCode string `json:"lockErrorCode"`
		}
		if json.Unmarshal(body, &decoded) == nil &&
			strings.EqualFold(strings.TrimSpace(decoded.LockErrorCode), "LOCK_PREEMPTED") {
			return true
		}
	}
	if rawErr != nil && strings.Contains(rawErr.Error(), "LOCK_PREEMPTED") {
		return true
	}
	return false
}

func releaseManagerWorkspaceLock(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	holderID string,
	fencingToken int64,
) (bool, error) {
	payload := map[string]any{
		"holderId":     holderID,
		"fencingToken": fencingToken,
	}
	body, _, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "release", payload)
	if err != nil {
		return false, err
	}
	var decoded struct {
		Released bool `json:"released"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false, err
	}
	return decoded.Released, nil
}

// callManagerWorkspaceLockEndpoint POSTs a lock action and returns the unwrapped
// `data` payload, the HTTP status code, and an error (if any). The status is
// returned even on error so callers can branch on 409 (preemption) before
// treating it as a generic failure. On 4xx/5xx the returned body is the raw
// (still-wrapped) response so callers can inspect data.lockErrorCode.
func callManagerWorkspaceLockEndpoint(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	action string,
	payload map[string]any,
) ([]byte, int, error) {
	baseURL := managerAPIBaseURL(cfg)
	if strings.TrimSpace(baseURL) == "" {
		return nil, 0, fmt.Errorf("manager API base URL is empty")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, 0, fmt.Errorf("workspace lock action is empty")
	}

	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/agent/device/" + url.PathEscape(strings.TrimSpace(deviceMAC)) + "/workspace-lock/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encodedPayload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &wrapper)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Return the unwrapped data (carries lockErrorCode for 409) alongside the
		// status and error so callers can distinguish preemption.
		errOut := fmt.Errorf("workspace-lock %s status=%d body=%s", action, resp.StatusCode, strings.TrimSpace(string(body)))
		if len(wrapper.Data) > 0 {
			return wrapper.Data, resp.StatusCode, errOut
		}
		return body, resp.StatusCode, errOut
	}

	if wrapper.Code != 0 {
		return wrapper.Data, resp.StatusCode, fmt.Errorf("workspace-lock %s api code=%d msg=%s", action, wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return []byte("{}"), resp.StatusCode, nil
	}
	return wrapper.Data, resp.StatusCode, nil
}

func liveKitWorkspaceLockLeaseTTL(cfg config.LiveKitServiceManagerAPIConfig) int {
	lockTimeout := int(liveKitWorkspaceLockTimeout(cfg).Seconds())
	if lockTimeout <= 0 {
		return 20
	}
	if lockTimeout < 20 {
		return 20
	}
	if lockTimeout > 120 {
		return 120
	}
	return lockTimeout
}

func clampPositiveInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func parseManagerWorkspaceLockFencingToken(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	token, err := strconv.ParseInt(value, 10, 64)
	if err != nil || token < 0 {
		return 0
	}
	return token
}
