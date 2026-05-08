package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const workspaceLockFilePath = ".picoclaw/device.lock"

type workspaceLock struct {
	path   string
	owner  string
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

type workspaceLockState struct {
	Owner       string    `json:"owner"`
	PID         int       `json:"pid"`
	AcquiredAt  time.Time `json:"acquiredAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

func acquireWorkspaceLock(workspace, owner string, waitTimeout, staleAfter time.Duration) (*workspaceLock, error) {
	workspace = strings.TrimSpace(workspace)
	owner = strings.TrimSpace(owner)
	if workspace == "" {
		return nil, fmt.Errorf("workspace path is empty")
	}
	if owner == "" {
		owner = "unknown-owner"
	}
	if waitTimeout <= 0 {
		waitTimeout = 10 * time.Second
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}

	lockPath := filepath.Join(workspace, workspaceLockFilePath)
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	deadline := time.Now().Add(waitTimeout)
	for {
		state := workspaceLockState{
			Owner:       owner,
			PID:         os.Getpid(),
			AcquiredAt:  time.Now().UTC(),
			HeartbeatAt: time.Now().UTC(),
		}
		payload, _ := json.MarshalIndent(state, "", "  ")

		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := f.Write(payload); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write workspace lock: %w", writeErr)
			}
			_ = f.Close()

			lock := &workspaceLock{
				path:   lockPath,
				owner:  owner,
				stopCh: make(chan struct{}),
				doneCh: make(chan struct{}),
			}
			go lock.heartbeatLoop()
			return lock, nil
		}

		if !os.IsExist(err) {
			return nil, fmt.Errorf("create workspace lock: %w", err)
		}

		existing, readErr := readWorkspaceLockState(lockPath)
		if readErr == nil {
			age := time.Since(existing.HeartbeatAt)
			if existing.HeartbeatAt.IsZero() || age > staleAfter {
				_ = os.Remove(lockPath)
				continue
			}
		}

		if time.Now().After(deadline) {
			if readErr == nil && existing.Owner != "" {
				return nil, fmt.Errorf("workspace lock busy (owner=%s)", existing.Owner)
			}
			return nil, fmt.Errorf("workspace lock busy")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (l *workspaceLock) Release() error {
	if l == nil {
		return nil
	}
	var releaseErr error
	l.once.Do(func() {
		close(l.stopCh)
		<-l.doneCh

		current, err := readWorkspaceLockState(l.path)
		if err == nil && current.Owner != "" && current.Owner != l.owner {
			return
		}
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			releaseErr = err
		}
	})
	return releaseErr
}

func (l *workspaceLock) heartbeatLoop() {
	defer close(l.doneCh)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			state := workspaceLockState{
				Owner:       l.owner,
				PID:         os.Getpid(),
				HeartbeatAt: time.Now().UTC(),
			}
			existing, err := readWorkspaceLockState(l.path)
			if err == nil {
				state.AcquiredAt = existing.AcquiredAt
				if state.AcquiredAt.IsZero() {
					state.AcquiredAt = time.Now().UTC()
				}
			} else {
				state.AcquiredAt = time.Now().UTC()
			}
			payload, _ := json.MarshalIndent(state, "", "  ")
			_ = os.WriteFile(l.path, payload, 0o600)
		}
	}
}

func readWorkspaceLockState(path string) (workspaceLockState, error) {
	var out workspaceLockState
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if len(data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
