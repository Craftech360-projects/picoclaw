package livekit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/voice/stt"
)

type fakeSTTStream struct {
	results chan stt.TranscriptEvent
	mu      sync.Mutex
	sent    int
	closed  bool
}

func newFakeSTTStream() *fakeSTTStream {
	return &fakeSTTStream{results: make(chan stt.TranscriptEvent, 4)}
}

func (f *fakeSTTStream) SendAudio(pcm []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("stream closed")
	}
	f.sent++
	return nil
}

func (f *fakeSTTStream) Results() <-chan stt.TranscriptEvent { return f.results }
func (f *fakeSTTStream) Finalize() error                     { return nil }

func (f *fakeSTTStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.results)
	}
	return nil
}

func (f *fakeSTTStream) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

// The bug this guards: a closed STT stream ended RunInbound, so the session could
// not hear anything for the rest of the call. Reopening must both hand back the
// new stream and rebind the holder, or the PCM writer keeps feeding the dead one.
func TestReopenSTTStreamSwapsTheHolder(t *testing.T) {
	dead := newFakeSTTStream()
	fresh := newFakeSTTStream()
	holder := newSTTStreamHolder(dead)

	ap := &AudioPipeline{
		sttHolder: holder,
		reopenSTT: func() (stt.TranscriptionStream, error) { return fresh, nil },
	}

	got, err := ap.reopenSTTStream(context.Background(), "s")
	if err != nil {
		t.Fatalf("reopenSTTStream() error = %v", err)
	}
	if got != stt.TranscriptionStream(fresh) {
		t.Fatal("reopen returned a stream other than the replacement")
	}
	if holder.Get() != stt.TranscriptionStream(fresh) {
		t.Fatal("holder still points at the dead stream; the writer would keep feeding it")
	}
}

// The rotation bug: a reopened stream was swapped into the holder but nothing
// re-pointed ParticipantState.sttStream or re-registered the empty-tap
// callback. With gemini's 9-minute TTL that turned a latent gap into a
// scheduled every-session failure — ResetBuffer kept going to the dead adapter,
// so the live socket never got an activityStart and, with manual activity
// detection, ignored the child's audio outright.
func TestReopenSTTStreamRebindsTurnPlumbing(t *testing.T) {
	dead := &fakePTTStream{}
	fresh := &fakePTTStream{}
	ps := &ParticipantState{
		identity:   "device-a",
		sessionKey: "livekit:device:a",
		sttStream:  dead,
		turnEvents: make(chan interface{}, 10),
	}
	pipeline, _ := newFallbackTestPipeline(t)
	pipeline.sttHolder = newSTTStreamHolder(dead)
	pipeline.reopenSTT = func() (stt.TranscriptionStream, error) { return fresh, nil }
	pipeline.rebindSTT = bindSTTStream(ps, pipeline)

	if _, err := pipeline.reopenSTTStream(context.Background(), "s"); err != nil {
		t.Fatalf("reopenSTTStream() error = %v", err)
	}

	// The press path, exactly as handleDataMessage drives it.
	stream, _ := ps.turnPlumbing()
	resetPTTBuffer(stream)
	if fresh.resets() != 1 {
		t.Fatalf("ResetBuffer calls on the reopened stream = %d, want 1", fresh.resets())
	}
	if dead.resets() != 0 {
		t.Fatalf("ResetBuffer calls on the dead stream = %d, want 0 — the turn boundary never followed the swap", dead.resets())
	}

	fresh.mu.Lock()
	handler := fresh.emptyResult
	fresh.mu.Unlock()
	if handler == nil {
		t.Fatal("the reopened stream has no empty-result handler; \"I didn't hear you\" can never fire again")
	}
}

// A provider rejecting us (bad key, quota) must fail the session rather than spin.
func TestReopenSTTStreamGivesUpAfterRepeatedFailure(t *testing.T) {
	holder := newSTTStreamHolder(newFakeSTTStream())
	var calls int
	ap := &AudioPipeline{
		sttHolder: holder,
		reopenSTT: func() (stt.TranscriptionStream, error) {
			calls++
			return nil, errors.New("unauthorized")
		},
	}

	start := time.Now()
	if _, err := ap.reopenSTTStream(context.Background(), "s"); err == nil {
		t.Fatal("expected an error after exhausting attempts")
	}
	if calls != 3 {
		t.Fatalf("reopen attempts = %d, want 3", calls)
	}
	// 250ms + 500ms + 1s of backoff, so it cannot be a busy loop.
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("gave up in %v; backoff is not being applied", elapsed)
	}
}

func TestReopenSTTStreamStopsOnCancelledContext(t *testing.T) {
	ap := &AudioPipeline{
		sttHolder: newSTTStreamHolder(newFakeSTTStream()),
		reopenSTT: func() (stt.TranscriptionStream, error) { return nil, errors.New("nope") },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ap.reopenSTTStream(ctx, "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// The writer must follow a swap, since it runs on the PCM track goroutine and
// used to hold its own copy of the stream.
func TestWriterFollowsHolderSwap(t *testing.T) {
	dead := newFakeSTTStream()
	fresh := newFakeSTTStream()
	holder := newSTTStreamHolder(dead)
	w := &sttStreamWriter{holder: holder, providerName: "fake"}

	if err := w.WriteSample(make([]int16, 160)); err != nil {
		t.Fatalf("WriteSample() error = %v", err)
	}
	if dead.sentCount() != 1 {
		t.Fatalf("dead stream sent = %d, want 1", dead.sentCount())
	}

	holder.Swap(fresh)
	if err := w.WriteSample(make([]int16, 160)); err != nil {
		t.Fatalf("WriteSample() after swap error = %v", err)
	}
	if fresh.sentCount() != 1 {
		t.Fatalf("fresh stream sent = %d, want 1 — writer did not follow the swap", fresh.sentCount())
	}
	if dead.sentCount() != 1 {
		t.Fatalf("dead stream sent = %d, want 1 — writer is still feeding the dead stream", dead.sentCount())
	}
}
