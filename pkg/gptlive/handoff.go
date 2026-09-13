package gptlive

import (
	"sync"
	"time"
)

// handoffQueue lets a producer that must never block (the read goroutine in
// session.go) hand values to a channel whose consumer might be slow to drain
// it. push always returns immediately: it appends the value and, only if that
// pushes the queue's total weight over its cap, drops enough of the OLDEST
// values to get back under it — never the newest. That is the whole point of
// the type: a consumer recovering from a stall is handed fresh data first,
// rather than working through an ever-growing backlog of stale data before it
// ever catches up. weightOf lets the same structure cap raw audio by byte
// count (a duration in seconds converts to a byte budget once the sample rate
// is known) and discrete events by a plain count of 1 per item.
//
// A single feeder goroutine (feedHandoff, below) is the sole consumer of
// drainAll; push and drainAll both take mu and never touch the destination
// channel themselves. That separation is what keeps a producer's push from
// ever blocking on whatever is slow downstream of that channel — the one
// property this whole type exists to guarantee.
type handoffQueue[T any] struct {
	mu       sync.Mutex
	items    []T
	weight   int64
	max      int64
	weightOf func(T) int64

	notify chan struct{} // capacity 1: "there is at least one item waiting"

	// onDrop, if set, is called synchronously from push (outside mu) whenever
	// a push had to drop items to stay under max. It is never called with
	// dropped item content, only counts — see session.go's WarnCF callers for
	// why (never log audio content or anything that might carry a key).
	onDrop func(droppedItems int, droppedWeight int64)
}

func newHandoffQueue[T any](max int64, weightOf func(T) int64, onDrop func(int, int64)) *handoffQueue[T] {
	return &handoffQueue[T]{max: max, weightOf: weightOf, onDrop: onDrop, notify: make(chan struct{}, 1)}
}

// push appends v, never blocking regardless of how far behind the feeder (or
// its destination channel's consumer) has fallen. At least the value just
// pushed is always kept, even if it alone exceeds max: dropping it too would
// mean push can silently discard the very thing it was asked to store, which
// is worse than briefly exceeding the budget by one oversized item.
func (q *handoffQueue[T]) push(v T) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.weight += q.weightOf(v)
	var droppedItems int
	var droppedWeight int64
	for q.weight > q.max && len(q.items) > 1 {
		oldest := q.items[0]
		q.items = q.items[1:]
		w := q.weightOf(oldest)
		q.weight -= w
		droppedItems++
		droppedWeight += w
	}
	q.mu.Unlock()
	if droppedItems > 0 && q.onDrop != nil {
		q.onDrop(droppedItems, droppedWeight)
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// drainAll removes and returns every item currently queued, in order. Called
// only from the feeder goroutine.
func (q *handoffQueue[T]) drainAll() []T {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	out := q.items
	q.items = nil
	q.weight = 0
	return out
}

// feedHandoff drains q into dst until stop is closed, then returns (closing
// done on the way out, always, via defer). The caller must guarantee no
// further push on q can occur once it closes stop, and must wait for done to
// close before it ever closes dst itself — session.go's closeChannelsWhenIdle
// is the one place that does both; see its comment for why that ordering is
// safe.
//
// flushOnStop decides what happens to whatever is STILL queued at the moment
// stop is actually observed:
//
//   - false (used for audio): abandon it, immediately, with no further
//     attempt to deliver. audioQueue is deliberately capped small (a few
//     seconds — see its own doc comment) specifically so a stalled consumer
//     never accumulates a backlog worth spending teardown time on; dropping
//     it here is the same trade overflow-dropping already makes, just
//     triggered by teardown instead of a full queue.
//   - true (used for events): deliver every already-queued item with a
//     blocking send before returning. Every caller in this codebase
//     guarantees push happens-before close(stop) for anything that must
//     still be delivered (closeChannelsWhenIdle only closes eventFeederStop
//     after toolWG.Wait() returns, itself a happens-after edge on every
//     emit() an executeCall goroutine could still make), so this never
//     misses an already-queued item — TestFatalProtocolErrorEmitsOnce and
//     TestCloseWaitsForInFlightToolBeforeClosingEventsChannel both depend on
//     a final Error/FunctionResult surviving exactly this handoff. This can
//     only actually block if dst's own buffer is completely full with nobody
//     ever reading it again, which the outer sessionCloseTimeout in
//     closeChannelsWhenIdle already bounds, the same backstop that already
//     covers a tool goroutine that never returns.
//
// finalFlush (used on every path that ends this loop, not just the "stop
// observed while idle" one) is what makes flushOnStop=true correct
// regardless of which of two simultaneously ready select cases Go happens to
// pick: it does not depend on having observed stop via any particular
// branch, it just unconditionally drains and (if flushOnStop) delivers
// whatever drainAll() — a plain mutex-guarded read, independent of notify —
// finds in q right now. A version that instead tried to special-case "notify
// was also ready" around the select would still lose an item abandoned
// mid-send inside the per-item select below; finalFlush does not have that
// gap because it re-reads q directly rather than trusting the select that
// got it here. This gap is exactly what TestFatalProtocolErrorEmitsOnce
// caught flaking once eventQueue was wired in.
func feedHandoff[T any](q *handoffQueue[T], dst chan<- T, stop <-chan struct{}, done chan struct{}, flushOnStop bool) {
	defer close(done)
	// finalFlush picks up anything still sitting in q — a plain mutex-guarded
	// read, independent of notify or of how this loop noticed stop — so it is
	// correct no matter which of two simultaneously ready select cases Go
	// happened to pick.
	finalFlush := func() {
		if !flushOnStop {
			return
		}
		for _, v := range q.drainAll() {
			dst <- v
		}
	}
	for {
		select {
		case <-q.notify:
			batch := q.drainAll()
			for i, v := range batch {
				select {
				case dst <- v:
				case <-stop:
					if flushOnStop {
						// batch has already been removed from q by drainAll
						// above, so finalFlush alone could not recover the
						// rest of it — deliver it here, with plain blocking
						// sends, before falling through to finalFlush for
						// anything pushed since.
						dst <- v
						for _, w := range batch[i+1:] {
							dst <- w
						}
					}
					finalFlush()
					return
				}
			}
		case <-stop:
			finalFlush()
			return
		}
	}
}

// dropRateLimiter batches "we just dropped something" reports so a sustained
// consumer stall logs at most once per interval instead of once per dropped
// chunk/event, while still reporting the true cumulative total once it does
// log — a rate limiter that only kept the latest drop's size would silently
// understate how much was actually lost during a long stall.
type dropRateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
	items    int64
	weight   int64
}

// add accumulates one drop report. It reports shouldLog=true, with the
// cumulative items/weight since the last report (then resets that
// accumulator), at most once per interval; every add in between just
// accumulates silently.
func (d *dropRateLimiter) add(items int, weight int64) (shouldLog bool, totalItems int64, totalWeight int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items += int64(items)
	d.weight += weight
	now := time.Now()
	if !d.last.IsZero() && now.Sub(d.last) < d.interval {
		return false, 0, 0
	}
	d.last = now
	totalItems, totalWeight = d.items, d.weight
	d.items, d.weight = 0, 0
	return true, totalItems, totalWeight
}
