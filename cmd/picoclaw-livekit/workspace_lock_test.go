package main

import (
	"testing"
	"time"
)

func TestWorkspaceLockAcquireAndRelease(t *testing.T) {
	workspace := t.TempDir()

	lock, err := acquireWorkspaceLock(workspace, "owner-a", 2*time.Second, 15*time.Second)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
}

func TestWorkspaceLockBlocksConcurrentAcquire(t *testing.T) {
	workspace := t.TempDir()

	lock, err := acquireWorkspaceLock(workspace, "owner-a", 2*time.Second, 15*time.Second)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	start := time.Now()
	_, err = acquireWorkspaceLock(workspace, "owner-b", 500*time.Millisecond, 15*time.Second)
	if err == nil {
		t.Fatal("expected second acquire to fail while first lock is held")
	}
	if time.Since(start) < 450*time.Millisecond {
		t.Fatalf("second acquire failed too quickly; expected retry wait, elapsed=%s", time.Since(start))
	}
}
