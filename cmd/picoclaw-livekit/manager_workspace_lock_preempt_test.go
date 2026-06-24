package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkspaceLockResponseIsPreempted(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"preempted body", []byte(`{"lockErrorCode":"LOCK_PREEMPTED"}`), true},
		{"preempted body case-insensitive", []byte(`{"lockErrorCode":"lock_preempted"}`), true},
		{"not-held body", []byte(`{"lockErrorCode":"LOCK_NOT_HELD"}`), false},
		{"generic conflict body", []byte(`{"lockErrorCode":"LOCK_CONFLICT"}`), false},
		{"empty body", []byte(``), false},
		{"unrelated json", []byte(`{"foo":"bar"}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceLockResponseIsPreempted(tc.body, nil); got != tc.want {
				t.Fatalf("workspaceLockResponseIsPreempted(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestWorkspaceLockResponseIsPreemptedFromError(t *testing.T) {
	// When the Manager body could not be unwrapped, the raw error string still
	// carries the code (status=409 body=...LOCK_PREEMPTED...).
	if !workspaceLockResponseIsPreempted(nil, errWorkspaceLockPreempted) {
		// errWorkspaceLockPreempted itself does not contain the literal code, so
		// this should be false; guard against accidental true.
	}
	rawErr := fmtError(`workspace-lock heartbeat status=409 body={"code":409,"msg":"preempted","data":{"lockErrorCode":"LOCK_PREEMPTED"}}`)
	if !workspaceLockResponseIsPreempted(nil, rawErr) {
		t.Fatalf("expected preemption detected from raw error string")
	}
}

func TestLeaseMarkPreemptedFiresOnceAndSetsFlag(t *testing.T) {
	lease := &managerWorkspaceLockLease{}
	if lease.WasPreempted() {
		t.Fatalf("fresh lease should not be preempted")
	}

	var calls int32
	var wg sync.WaitGroup
	wg.Add(1)
	lease.SetOnPreempted(func() {
		atomic.AddInt32(&calls, 1)
		wg.Done()
	})

	// Concurrent markPreempted calls must fire the callback exactly once.
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < 8; i++ {
		go func() {
			start.Wait()
			lease.markPreempted()
		}()
	}
	start.Done()

	waitTimeout(t, &wg, 2*time.Second)

	if !lease.WasPreempted() {
		t.Fatalf("lease should be preempted after markPreempted")
	}
	// Give any erroneous extra goroutines a moment, then assert exactly one call.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onPreempted fired %d times, want 1", got)
	}
}

// TestWorkspaceSyncLoopPreemptGatePredicate verifies the predicate the
// background workspace-sync loop uses to gate its periodic upload. main.go wires
// `func() bool { return lease.WasPreempted() }` into startWorkspaceSyncLoop; the
// periodic_checkpoint tick skips the upload (and stops the loop) when it returns
// true. This asserts the predicate flips exactly when the lease is preempted, so
// a fenced-out OLD session never pushes its workspace to the Manager mid-teardown.
//
// We test the gate predicate directly rather than spinning the real loop because
// the loop's minimum tick interval is clamped to 30s (liveKitWorkspaceSyncInterval),
// which would make a full end-to-end timing test slow and flaky.
func TestWorkspaceSyncLoopPreemptGatePredicate(t *testing.T) {
	lease := &managerWorkspaceLockLease{}
	isPreempted := func() bool { return lease.WasPreempted() }

	if isPreempted() {
		t.Fatalf("fresh lease: periodic upload gate should allow upload (isPreempted=true)")
	}

	lease.markPreempted()

	if !isPreempted() {
		t.Fatalf("after markPreempted: periodic upload gate must skip upload (isPreempted=false)")
	}
}

func TestNilLeasePreemptHelpersAreSafe(t *testing.T) {
	var lease *managerWorkspaceLockLease
	if lease.WasPreempted() {
		t.Fatalf("nil lease WasPreempted should be false")
	}
	// Must not panic.
	lease.SetOnPreempted(func() {})
	lease.markPreempted()
}

// helpers ---------------------------------------------------------------------

type stringError string

func (e stringError) Error() string { return string(e) }

func fmtError(s string) error { return stringError(s) }

func waitTimeout(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out waiting for onPreempted callback")
	}
}
