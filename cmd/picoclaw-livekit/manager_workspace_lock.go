package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

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
}

func (l *managerWorkspaceLockLease) startHeartbeat(interval time.Duration) {
	if l == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
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
	deadline := time.Now().Add(waitTimeout)

	leaseTTLSeconds = clampPositiveInt(leaseTTLSeconds, 20, 120)
	for {
		result, err := acquireManagerWorkspaceLock(ctx, cfg, deviceMAC, holderID, leaseTTLSeconds, 2)
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
			lease.startHeartbeat(5 * time.Second)
			return lease, nil
		}
		if time.Now().After(deadline) {
			currentOwner := ""
			if result.Current != nil {
				currentOwner = result.Current.HolderID
			}
			if currentOwner != "" {
				return nil, fmt.Errorf("manager workspace lock busy (owner=%s)", currentOwner)
			}
			return nil, fmt.Errorf("manager workspace lock busy")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func acquireManagerWorkspaceLock(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	holderID string,
	leaseTTLSeconds int,
	staleGraceSeconds int,
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
	}
	body, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "acquire", payload)
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
	body, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "heartbeat", payload)
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
	body, err := callManagerWorkspaceLockEndpoint(ctx, cfg, deviceMAC, "release", payload)
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

func callManagerWorkspaceLockEndpoint(
	ctx context.Context,
	cfg config.LiveKitServiceManagerAPIConfig,
	deviceMAC string,
	action string,
	payload map[string]any,
) ([]byte, error) {
	baseURL := managerAPIBaseURL(cfg)
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("manager API base URL is empty")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return nil, fmt.Errorf("workspace lock action is empty")
	}

	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/agent/device/" + url.PathEscape(strings.TrimSpace(deviceMAC)) + "/workspace-lock/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encodedPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceKey := strings.TrimSpace(managerAPIServiceKey()); serviceKey != "" {
		req.Header.Set("X-Service-Key", serviceKey)
		req.Header.Set("Authorization", "Bearer "+serviceKey)
	}

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("workspace-lock %s status=%d body=%s", action, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Code != 0 {
		return nil, fmt.Errorf("workspace-lock %s api code=%d msg=%s", action, wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return []byte("{}"), nil
	}
	return wrapper.Data, nil
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
