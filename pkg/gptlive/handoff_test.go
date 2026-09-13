package gptlive

import (
	"testing"
	"time"
)

// TestHandoffQueuePushNeverBlocks is the core structural guarantee handoffQueue
// exists to provide: the read goroutine in session.go must never be able to
// stall on a slow (or entirely absent) consumer downstream of Audio()/Events().
// Nothing here ever drains the queue (no feedHandoff goroutine is even
// started), so if push could ever block on a full internal buffer, this would
// hang.
func TestHandoffQueuePushNeverBlocks(t *testing.T) {
	q := newHandoffQueue[[]byte](1024, func(b []byte) int64 { return int64(len(b)) }, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100000; i++ {
			q.push(make([]byte, 64))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked; it must never block regardless of how much is queued or how slow (or absent) the drain is")
	}
}

// TestHandoffQueueDropsOldestOnOverflowAndCounts covers the memory-honesty half
// of the fix: once a queue is over its weight cap, the OLDEST items must be
// dropped (never the newest — dropping new audio would stutter exactly like
// the bug this fixes), and every drop must be reported through onDrop so a
// caller can log/count it.
func TestHandoffQueueDropsOldestOnOverflowAndCounts(t *testing.T) {
	var droppedItems int
	var droppedWeight int64
	q := newHandoffQueue[int](3, func(int) int64 { return 1 }, func(items int, weight int64) {
		droppedItems += items
		droppedWeight += weight
	})
	for i := 1; i <= 5; i++ {
		q.push(i)
	}
	got := q.drainAll()
	want := []int{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("drainAll() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drainAll() = %v, want %v (the OLDEST items must be dropped, not the newest)", got, want)
		}
	}
	if droppedItems != 2 {
		t.Errorf("dropped items = %d, want 2", droppedItems)
	}
	if droppedWeight != 2 {
		t.Errorf("dropped weight = %d, want 2", droppedWeight)
	}
}

// TestHandoffQueueNeverDropsTheOnlyItemEvenIfOversized guards push's own edge
// case: a single value whose weight alone exceeds max must still be kept —
// dropping the very thing push was just asked to store would be worse than
// briefly exceeding the budget by one oversized item.
func TestHandoffQueueNeverDropsTheOnlyItemEvenIfOversized(t *testing.T) {
	var dropped int
	q := newHandoffQueue[[]byte](4, func(b []byte) int64 { return int64(len(b)) }, func(items int, _ int64) { dropped += items })
	q.push(make([]byte, 100)) // alone, already far over the cap of 4
	got := q.drainAll()
	if len(got) != 1 {
		t.Fatalf("drainAll() returned %d items, want 1 (an oversized single item must still be kept)", len(got))
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0: the only item queued must never be dropped to make room for itself", dropped)
	}
}

// TestFeedHandoffDrainsInOrderAndStopsWithoutSendingAfterStop exercises the
// feeder side of the handoff with flushOnStop=false (the audio configuration):
// values must be delivered in order, and once stop is closed the feeder must
// return promptly — including abandoning a send it was already blocked on —
// and never touch dst again afterward. That last part is what session.go's
// closeChannelsWhenIdle relies on to close s.audio/s.events safely once done
// has closed.
func TestFeedHandoffDrainsInOrderAndStopsWithoutSendingAfterStop(t *testing.T) {
	q := newHandoffQueue[int](1000, func(int) int64 { return 1 }, nil)
	dst := make(chan int) // unbuffered: forces feedHandoff to block on sends until this test reads
	stop := make(chan struct{})
	done := make(chan struct{})
	go feedHandoff(q, dst, stop, done, false)

	q.push(1)
	q.push(2)
	q.push(3)
	if got := <-dst; got != 1 {
		t.Fatalf("first drained value = %d, want 1", got)
	}
	if got := <-dst; got != 2 {
		t.Fatalf("second drained value = %d, want 2", got)
	}

	// The feeder is now blocked trying to send 3 (nobody is reading dst).
	// Queue more work, then stop: the feeder must give up on that blocked
	// send and return, rather than hang or deliver anything more.
	q.push(4)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feedHandoff did not return after stop was closed")
	}

	// Safe only because <-done above proves feedHandoff has already stopped
	// touching dst; this is the same guarantee closeChannelsWhenIdle leans on
	// before it closes s.audio/s.events for real.
	close(dst)
	if v, ok := <-dst; ok {
		t.Fatalf("feedHandoff sent on dst after stop was closed: got %d", v)
	}
}

// TestFeedHandoffFlushOnStopDeliversAlreadyQueuedItems exercises the
// flushOnStop=true configuration (used for events): this is the direct
// regression test for the race TestFatalProtocolErrorEmitsOnce caught once
// eventQueue was wired in — a value pushed strictly before stop is closed
// must always be delivered, even though push's own notify signal and stop's
// close can both be simultaneously ready by the time this goroutine's select
// runs (Go's select picks between ready cases pseudo-randomly, so a naive
// "notify vs stop" select drops the value on whichever runs picked stop).
// dst is buffered here specifically so the send can complete instantly no
// matter which select case fires first, isolating the delivery guarantee
// from any blocking/timing concern.
func TestFeedHandoffFlushOnStopDeliversAlreadyQueuedItems(t *testing.T) {
	for i := 0; i < 200; i++ { // many iterations: the race is not on every run
		q := newHandoffQueue[int](10, func(int) int64 { return 1 }, nil)
		dst := make(chan int, 10)
		stop := make(chan struct{})
		done := make(chan struct{})

		q.push(1) // push happens-before close(stop), exactly like emit()'s push before closeChannelsWhenIdle's close(eventFeederStop)
		close(stop)
		go feedHandoff(q, dst, stop, done, true)

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: feedHandoff did not return", i)
		}
		select {
		case got := <-dst:
			if got != 1 {
				t.Fatalf("iteration %d: delivered %d, want 1", i, got)
			}
		default:
			t.Fatalf("iteration %d: the value pushed before stop was closed was never delivered", i)
		}
	}
}
