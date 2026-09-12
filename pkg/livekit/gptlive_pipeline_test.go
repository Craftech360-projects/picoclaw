package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/media-sdk"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/gptlive"
)

func TestPCM16SampleToBytesIsLittleEndian(t *testing.T) {
	got := pcm16ToBytes(media.PCM16Sample{1, -2})
	want := []byte{0x01, 0x00, 0xFE, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got % x want % x", got, want)
	}
	back := bytesToPCM16(got)
	if len(back) != 2 || back[0] != 1 || back[1] != -2 {
		t.Errorf("round trip: %v", back)
	}
}

func TestPipelineRecordsFinalTranscriptsOnly(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hel", Final: false})
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hello", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "Hi ", Text: "Hi "})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "there", Text: "Hi there"})
	p.onBurstClose()
	snap := p.TranscriptSnapshot()
	// The brief's original test named this constant chatTypeAgent; agent_bridge.go
	// already defines chatTypeAssistant with the same meaning (and the same value
	// the cascade's own TranscriptSnapshot uses), so this asserts against that
	// existing constant instead of introducing a duplicate name for it.
	if len(snap) != 2 || snap[0].Content != "hello" || snap[0].ChatType != chatTypeUser || snap[1].Content != "Hi there" || snap[1].ChatType != chatTypeAssistant {
		t.Errorf("snapshot: %+v", snap)
	}
}

func TestPipelineIgnoresInterimUserTranscriptInSnapshot(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "not yet done", Final: false})
	if snap := p.TranscriptSnapshot(); len(snap) != 0 {
		t.Errorf("interim transcript must not be recorded: %+v", snap)
	}
}

// TestPipelineVoiceSecondsIsMonotonic covers the final whole-branch review's
// Critical 2. VoiceUsage.Seconds and Closed.VoiceSeconds are both cumulative
// session totals, not deltas, so summing them would overcount — but the
// previous version of this test asserted plain assignment, which is what let
// the bug it now covers ship: the Closed case overwrote the running total
// UNCONDITIONALLY, including with zero. gptlive.Session's handleEvent sets
// VoiceSeconds to 0 whenever session.closed arrives with no usage object at
// all (ServerEvent.Usage is a pointer), and leave() is deliberately ordered
// Close -> WaitForEventsDrain -> persist precisely so that is the LAST write
// before persistGPTLiveSession reads it. Every session would have posted
// sessionDurationSeconds 0, silently. The same line is the reconnect hazard:
// a fresh server session's first usage.updated reports its own seconds, not
// the call's, and would have replaced the pre-blip total.
func TestPipelineVoiceSecondsIsMonotonic(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.VoiceUsage{Seconds: 5})
	p.onEvent(gptlive.VoiceUsage{Seconds: 12})
	if got := p.VoiceSeconds(); got != 12 {
		t.Errorf("VoiceSeconds() = %v, want 12 (latest, not summed)", got)
	}
	// A reconnect's restarted counter must not throw the session's seconds away.
	p.onEvent(gptlive.VoiceUsage{Seconds: 2})
	if got := p.VoiceSeconds(); got != 12 {
		t.Errorf("VoiceSeconds() after a lower (post-reconnect) usage report = %v, want 12", got)
	}
	p.onEvent(gptlive.Closed{Reason: "close_requested", VoiceSeconds: 20})
	if got := p.VoiceSeconds(); got != 20 {
		t.Errorf("VoiceSeconds() after Closed = %v, want 20", got)
	}
	// The failure this exists for: session.closed with no usage object.
	p.onEvent(gptlive.Closed{Reason: "close_requested", VoiceSeconds: 0})
	if got := p.VoiceSeconds(); got != 20 {
		t.Fatalf("VoiceSeconds() after a Closed carrying no usage = %v, want 20 — a usage-less "+
			"session.closed must never erase the session's billed seconds", got)
	}
}

// TestPersistedVoiceSecondsSurviveAUsageLessClose is the end-to-end half of
// Critical 2, through the exact sequence leave() runs (Close ->
// WaitForEventsDrain -> read), against a fake server whose session.closed
// carries NO usage object — the shape this repo cannot rule out, and the one
// our other fake servers hide by always hard-coding a number.
func TestPersistedVoiceSecondsSurviveAUsageLessClose(t *testing.T) {
	url := newFakeGPTLiveServerWithoutCloseUsage(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000})
	if err := p.finishStart(context.Background(), sess); err != nil {
		t.Fatalf("finishStart() error = %v", err)
	}
	// A usage report mid-session, exactly as usage.updated would deliver it.
	p.onEvent(gptlive.VoiceUsage{Seconds: 42})

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	p.Close(closeCtx)
	cancel()
	p.WaitForEventsDrain(2 * time.Second)

	if got := p.VoiceSeconds(); got != 42 {
		t.Fatalf("VoiceSeconds() = %v, want 42 — the final session.closed carried no usage object and "+
			"must not have overwritten the session's billed seconds with zero", got)
	}
}

func TestPipelineBackendTokensSumsAcrossResponses(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.BackendUsage{Total: 100, Input: 60, Output: 40})
	p.onEvent(gptlive.BackendUsage{Total: 50, Input: 30, Output: 20})
	// BackendUsage.Total/Input/Output are all per completed backend response
	// (one per delegation), so the session total is the sum across every
	// response. Input/Output are asserted here too (Task 13 review, Critical
	// 1): persistGPTLiveSession needs them specifically, since sendUsageSummary
	// skips its POST entirely when both are zero, so tracking only Total would
	// make every gptlive session's usage silently never reach the manager API.
	if got := p.BackendTokens(); got != 150 {
		t.Errorf("BackendTokens() = %v, want 150 (summed across responses)", got)
	}
	if got := p.BackendInputTokens(); got != 90 {
		t.Errorf("BackendInputTokens() = %v, want 90 (summed across responses)", got)
	}
	if got := p.BackendOutputTokens(); got != 60 {
		t.Errorf("BackendOutputTokens() = %v, want 60 (summed across responses)", got)
	}
}

// TestNewRoomSessionConstructsGPTLivePipelineBeforeJoin covers Task 12 review's
// Critical 1: handleTrackSubscribed decided whether to run the GPT-Live branch
// or fall through to the cascade by checking rs.gptlive, which used to be
// assigned only after Join's call to pipeline.Start() returned — and Start
// does a blocking gptlive.Dial (up to a 10s timeout, 3 retries at 2s).
// OnTrackSubscribed is registered before ConnectToRoomWithToken, so a device
// already in the room could fire it during that whole window, while
// rs.gptlive was still nil, and fall through to opening a cascade STT stream
// instead — silently starving the GPT-Live session of all microphone audio
// for the entire call.
//
// The fix constructs the pipeline object synchronously in NewRoomSession,
// before Join (and so before any callback) can possibly run. This test calls
// only NewRoomSession — deliberately never Join — and checks rs.gptlive is
// already non-nil, which is exactly the property that keeps
// handleTrackSubscribed's branch decision correct regardless of how early a
// track is subscribed relative to Start's Dial completing.
func TestNewRoomSessionConstructsGPTLivePipelineBeforeJoin(t *testing.T) {
	rs, err := NewRoomSession(RoomSessionConfig{
		RoomInfo:  &lkproto.Room{Name: "room-a"},
		ServerURL: "ws://localhost:7880",
		GPTLive:   &GPTLiveSessionSpec{SampleRate: 24000},
	})
	if err != nil {
		t.Fatalf("NewRoomSession() error = %v", err)
	}
	if rs.gptlive == nil {
		t.Fatal("rs.gptlive must be constructed synchronously by NewRoomSession, before Join ever runs — " +
			"otherwise handleTrackSubscribed can observe it nil and fall through to the cascade")
	}
	if rs.gptLiveSpec == nil {
		t.Fatal("rs.gptLiveSpec must also be set synchronously: handleTrackSubscribed gates on it specifically " +
			"because it (unlike rs.gptlive's readiness) can never lag behind construction")
	}
}

// TestNewRoomSessionLeavesGPTLiveNilForACascadeSession guards the other
// direction: a config with no GPTLive spec must not get a pipeline, or every
// existing cascade session would suddenly take the GPT-Live branch.
func TestNewRoomSessionLeavesGPTLiveNilForACascadeSession(t *testing.T) {
	rs, err := NewRoomSession(RoomSessionConfig{
		RoomInfo:  &lkproto.Room{Name: "room-b"},
		ServerURL: "ws://localhost:7880",
	})
	if err != nil {
		t.Fatalf("NewRoomSession() error = %v", err)
	}
	if rs.gptlive != nil || rs.gptLiveSpec != nil {
		t.Fatal("a cascade session (no RoomSessionConfig.GPTLive) must not get a gptLivePipeline")
	}
}

// TestDriveSegmenterSerializesFeedAndTick covers Task 12 review's Critical 2:
// segment.go documents, verbatim, that Segmenter.Feed and Segmenter.Tick "must
// be serialized... not safe for concurrent use" — the type has no mutex of its
// own. The pipeline used to call Feed from pumpAudioOut and Tick from a
// separate tickSegmenter goroutine, so a trailing Feed could race a Tick and
// close the same burst twice, silently dropping the agent transcript the
// second pass would have carried.
//
// driveSegmenter is the fix: it is the one loop, run by exactly one goroutine,
// that reads both the audio and the ticks channel and is the sole caller of
// Feed/Tick. This test drives it directly with two independent goroutines
// pushing frames and ticks concurrently — the same shape the old two-goroutine
// wiring had — under the race detector, and checks the one hard invariant a
// double-close would break: every open burst is closed at most once.
func TestDriveSegmenterSerializesFeedAndTick(t *testing.T) {
	var opens, closes int32
	p := &gptLivePipeline{spec: GPTLiveSessionSpec{SampleRate: 8000}}
	p.seg = gptlive.NewSegmenter(
		func() { atomic.AddInt32(&opens, 1) },
		func() { atomic.AddInt32(&closes, 1) },
	)

	// 800 samples at 8kHz = 100ms per frame (frameDur derives duration from
	// frame length and p.spec.SampleRate, so this is exact and independent of
	// real wall-clock timing).
	loudSamples := make(media.PCM16Sample, 800)
	for i := range loudSamples {
		loudSamples[i] = 30000
	}
	loud := pcm16ToBytes(loudSamples)
	quiet := pcm16ToBytes(make(media.PCM16Sample, 800)) // all-zero: silence

	audio := make(chan []byte, 8)
	ticks := make(chan time.Time, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.driveSegmenter(ctx, audio, ticks)
		close(done)
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	// Producer A: open the burst with one loud frame, then feed quiet frames —
	// 5 of them (5 x 100ms = 500ms of accumulated quiet) deterministically
	// closes it via Feed's own gate logic, independent of wall-clock timing.
	go func() {
		defer wg.Done()
		select {
		case audio <- loud:
		case <-ctx.Done():
			return
		}
		for i := 0; i < 10; i++ {
			select {
			case audio <- quiet:
			case <-ctx.Done():
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	// Producer B: a concurrent, independent ticker — exactly the shape the old
	// tickSegmenter goroutine had, now racing Producer A's Feed calls into the
	// same driveSegmenter loop instead of calling Segmenter.Tick directly.
	go func() {
		defer wg.Done()
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for i := 0; i < 200; i++ {
			select {
			case now := <-t.C:
				select {
				case ticks <- now:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()

	// Backstop: force a Tick well past the 800ms idle window in case Feed's own
	// path somehow left the burst open, so the test doesn't hang on the final
	// assertion regardless of scheduler timing.
	time.Sleep(50 * time.Millisecond)
	select {
	case ticks <- time.Now().Add(2 * time.Second):
	default:
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	o, c := atomic.LoadInt32(&opens), atomic.LoadInt32(&closes)
	if o == 0 {
		t.Fatal("expected at least one burst to open")
	}
	if c > o {
		t.Fatalf("closes (%d) exceeded opens (%d): a burst was closed more than once — exactly the "+
			"double-close that running Feed and Tick on separate goroutines used to allow", c, o)
	}
	if c != o {
		t.Fatalf("closes (%d) != opens (%d): the open burst was never closed", c, o)
	}
}

// newMinimalFakeGPTLiveServer answers just enough of the GPT-Live protocol —
// session.start with session.started, session.close with session.closed — to
// let gptlive.Dial succeed and gptlive.Session.Close return promptly, so
// TestFinishStartSelfClosesWhenContextAlreadyCancelled can obtain a real
// *gptlive.Session without a slow dial or a 5-second Close timeout. This is
// not pkg/gptlive's own fakeLive test helper (unexported to that package's
// own test binary, not importable here) — just the minimum needed here.
func newMinimalFakeGPTLiveServer(t *testing.T) string {
	t.Helper()
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			switch ev["type"] {
			case "session.start":
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
			case "session.close":
				_ = conn.WriteJSON(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": 0}})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestFinishStartSelfClosesWhenContextAlreadyCancelled covers Task 12 review
// round 2's New Important 1: because gptLivePipeline now exists from
// NewRoomSession (Critical 1's fix), leave() can call Close while Start is
// still blocked inside gptlive.Dial (up to ~16s with retries). Close finds
// p.sess and p.cancel both nil at that point and does nothing. Before this
// fix, Start would then go on to assign p.sess, spawn four goroutines on a
// context nothing could ever cancel (closeOnce already spent, rs.gptlive
// already nil'd), and the greeting timer would have the model speak into a
// room that no longer exists three seconds later.
//
// finishStart is the extracted piece of Start that runs after Dial returns a
// live session; this test calls it directly with an already-cancelled ctx
// (standing in for rs.ctx after leave() has run) and a real *gptlive.Session
// obtained from a minimal fake server. It asserts both required outcomes:
// finishStart reports an error (so Join propagates it and the worker's normal
// "Join failed" cleanup runs), and no pump goroutine was actually spawned —
// checked by pushing directly onto p.publish and confirming nothing consumes
// it, since only pumpPublish ever reads that channel.
func TestFinishStartSelfClosesWhenContextAlreadyCancelled(t *testing.T) {
	url := newMinimalFakeGPTLiveServer(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer sess.Close(context.Background())

	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stands in for leave()'s rs.cancel(), already called before Close

	if err := p.finishStart(ctx, sess); err == nil {
		t.Fatal("finishStart() with an already-cancelled context must return an error, not spawn the pumps")
	}
	if p.startErr == nil {
		t.Fatal("finishStart's self-close branch must set p.startErr to a non-nil error before it ever " +
			"closes p.ready, or a handleTrackSubscribed goroutine already blocked on <-p.ready reads the " +
			"nil zero value and wires the room's mic into a pipeline that is being closed right now (Task " +
			"13 review, carried forward from Task 12)")
	}

	// If pumpPublish were running (i.e. the pumps leaked), it would consume
	// this within microseconds; nothing should be listening on p.publish at all.
	consumed := make(chan struct{})
	p.publish <- func() { close(consumed) }
	select {
	case <-consumed:
		t.Fatal("a pump goroutine consumed from p.publish — the pumps were not supposed to be spawned")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestFinishStartSelfCloseWaiterNeverObservesNilStartErr reproduces
// handleTrackSubscribed's exact shape (room_session.go): a goroutine blocked
// on <-p.ready, which reads p.startErr the instant the receive unblocks, with
// no lock or other synchronization of its own — that IS the synchronization,
// per p.ready's own doc comment. Before the fix, finishStart closed p.ready
// unconditionally before ever checking ctx.Done(), so this waiter could wake
// up, see p.startErr's nil zero value (nothing had set it yet in the
// self-close branch), and proceed to wire the room's remote track into a
// pipeline finishStart was about to close underneath it. The waiter goroutine
// here is started BEFORE finishStart is even called, so it is genuinely
// racing the close, not just checking the field afterwards.
func TestFinishStartSelfCloseWaiterNeverObservesNilStartErr(t *testing.T) {
	url := newMinimalFakeGPTLiveServer(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer sess.Close(context.Background())

	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	waiterErr := make(chan error, 1)
	go func() {
		<-p.ready
		waiterErr <- p.startErr
	}()

	_ = p.finishStart(ctx, sess)

	select {
	case got := <-waiterErr:
		if got == nil {
			t.Fatal("waiter observed a nil startErr right after <-p.ready unblocked, in the self-close path")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter never saw p.ready close")
	}
}

// TestConcurrentAccessDuringFinishStartDoesNotRace covers Task 12 review round
// 2's New Important 2: Start (now finishStart) wrote p.sess/p.cancel without a
// lock while Close, Greet, SayGoodbyeAndWait and the quiz OnDirective closure
// read them from other goroutines. This was reachable in theory before this
// fix round, but became reachable in practice once Critical 1's fix made
// leave() (via Close) — and a "ready_for_greeting" or "end_prompt" data
// message (via Greet/SayGoodbyeAndWait) — able to genuinely run concurrently
// with the dial window. This races real calls to Greet, SayGoodbyeAndWait and
// Close against finishStart's assignment; the only assertion that matters is
// what -race reports (none, once every reader/writer goes through p.mu).
func TestConcurrentAccessDuringFinishStartDoesNotRace(t *testing.T) {
	url := newMinimalFakeGPTLiveServer(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}

	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000, Persona: GPTLivePersona{Greeting: "hi"}})

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		_ = p.finishStart(context.Background(), sess)
	}()
	go func() {
		defer wg.Done()
		p.Greet()
	}()
	go func() {
		defer wg.Done()
		p.SayGoodbyeAndWait(context.Background(), "bye", 50*time.Millisecond)
	}()
	go func() {
		defer wg.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.Close(closeCtx)
	}()
	wg.Wait()

	// Whichever interleaving occurred, make sure the session and any pumps
	// that did get spawned are torn down before the test ends. Close is
	// idempotent-safe to call again.
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.Close(closeCtx)
}

// TestPublishAgentStateOnRoomMatchesPublishAgentStateForNilRoom covers Task 12
// review round 2's New Important 3: setState's closure used to call
// p.rs.PublishAgentState(prev, next), which internally re-reads rs.room (a
// second, separate unguarded read from the one setState already snapshotted
// via roomSnapshot) — the same nil-check-then-use shape Critical 3 fixed
// elsewhere, reachable here because this runs on the long-lived pumpPublish
// goroutine. publishAgentStateOnRoom takes the room as a parameter instead, so
// this checks it still behaves the same as PublishAgentState for the one case
// both need to get right: a nil room is an error, not a panic.
// Minor 1 of the final whole-branch review then made RoomSession.PublishAgentState
// a one-line wrapper over publishAgentStateOnRoom, so the two can no longer drift:
// a payload change now reaches cascade and gptlive sessions alike, instead of
// only whichever copy was edited. This asserts the one observable behaviour the
// cascade depends on and the wrapper had to preserve exactly — a room that is
// not ready is an error, never a panic — for both entry points.
func TestPublishAgentStateOnRoomMatchesPublishAgentStateForNilRoom(t *testing.T) {
	if err := publishAgentStateOnRoom(nil, "listening", "speaking"); err == nil {
		t.Error("expected an error for a nil room, matching RoomSession.PublishAgentState's own nil-room behavior")
	}
	rs := &RoomSession{roomInfo: &lkproto.Room{Name: "room-no-room"}}
	err := rs.PublishAgentState("listening", "speaking")
	if err == nil {
		t.Fatal("RoomSession.PublishAgentState with no room must still return an error, as it always did")
	}
	if err.Error() != "room local participant not ready" {
		t.Errorf("PublishAgentState nil-room error = %q, want the unchanged %q",
			err.Error(), "room local participant not ready")
	}
}

// newFakeGPTLiveServerWithUsage is newMinimalFakeGPTLiveServer plus a
// caller-chosen VoiceSeconds on the session.closed reply, so
// TestWaitForEventsDrainSynchronizesBeforeReadingVoiceSeconds can tell
// whether the value it reads is the real final one or a stale zero.
func newFakeGPTLiveServerWithUsage(t *testing.T, seconds float64) string {
	t.Helper()
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			switch ev["type"] {
			case "session.start":
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
			case "session.close":
				_ = conn.WriteJSON(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": seconds}})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestWaitForEventsDrainSynchronizesBeforeReadingVoiceSeconds covers Task
// 12's "carried forward, not fixed" review note, exercised through the real
// dial + fake server + finishStart/Close/WaitForEventsDrain path leave() uses,
// end to end. It is NOT a reliable flake reproduction on its own: on a fast
// loopback connection the Closed{VoiceSeconds} event is typically already
// sitting in the channel and gets drained by pumpEvents' normal select case
// well before ctx.Done() (fired only after sess.Close returns) ever becomes
// ready, so removing the WaitForEventsDrain call below does not reliably fail
// this test locally (confirmed: 5/20 manual runs, all passed) — the race it
// guards against needs the scheduler to leave pumpEvents unscheduled across
// that whole window, which -race's overhead makes more likely but still
// doesn't guarantee (Task 13 review, Important 3: an earlier version of this
// comment claimed the opposite). What this test actually verifies is the
// happens-before contract: after WaitForEventsDrain returns, pumpEvents has
// unconditionally finished (including any drain, per drainEvents' now-
// deterministic loop), so VoiceSeconds() cannot observe a stale value.
// TestDrainEventsProcessesBufferedEventsBeforeReturning below is the
// deterministic counterpart that actually forces the buffered-event-at-
// cancel-time window, with no scheduler luck involved.
func TestWaitForEventsDrainSynchronizesBeforeReadingVoiceSeconds(t *testing.T) {
	const wantSeconds = 7.5
	for i := 0; i < 20; i++ {
		url := newFakeGPTLiveServerWithUsage(t, wantSeconds)
		sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
		if err != nil {
			t.Fatalf("Dial() error = %v", err)
		}

		p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000})
		if err := p.finishStart(context.Background(), sess); err != nil {
			t.Fatalf("finishStart() error = %v", err)
		}

		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		p.Close(closeCtx)
		cancel()
		// This is the exact sequence leave() uses (Close, then
		// WaitForEventsDrain, then read VoiceSeconds/BackendTokens/
		// TranscriptSnapshot) — see the doc comment above for what this call
		// does and does not prove on its own.
		p.WaitForEventsDrain(2 * time.Second)

		if got := p.VoiceSeconds(); got != wantSeconds {
			t.Fatalf("iteration %d: VoiceSeconds() = %v, want %v (final Closed event was not drained before reading)", i, got, wantSeconds)
		}
	}
}

// TestDrainEventsProcessesBufferedEventsBeforeReturning is the deterministic
// reproduction Important 3 of the Task 13 review asked for: force the exact
// window pumpEvents' old `case <-ctx.Done(): return` could lose an event in —
// one or more values already sitting in the channel's buffer at the instant
// it is closed — with a synthetic channel instead of racing a real dial
// against a real Close, so this fails on every run without the fix and passes
// on every run with it, no scheduler luck required either way.
func TestDrainEventsProcessesBufferedEventsBeforeReturning(t *testing.T) {
	events := make(chan gptlive.Event, 2)
	events <- gptlive.BackendUsage{Total: 5}
	events <- gptlive.Closed{Reason: "close_requested", VoiceSeconds: 20}
	close(events) // mirrors run()/closeChannelsWhenIdle: buffered values survive a closed channel.

	var got []gptlive.Event
	drainEvents(events, func(ev gptlive.Event) { got = append(got, ev) })

	if len(got) != 2 {
		t.Fatalf("drainEvents delivered %d events, want 2 (both buffered values, not just whichever the channel-close raced against)", len(got))
	}
	if _, ok := got[0].(gptlive.BackendUsage); !ok {
		t.Errorf("got[0] = %#v, want the BackendUsage sent first", got[0])
	}
	closed, ok := got[1].(gptlive.Closed)
	if !ok || closed.VoiceSeconds != 20 {
		t.Errorf("got[1] = %#v, want gptlive.Closed{VoiceSeconds: 20} sent last", got[1])
	}
}

// newFakeGPTLiveServerWithoutCloseUsage answers session.close with a
// session.closed that carries NO usage object at all — which is what makes
// gptlive.Session emit Closed{VoiceSeconds: 0}. Every other fake server in
// this file hard-codes a number there, which is exactly why Critical 2 was
// invisible to the suite.
func newFakeGPTLiveServerWithoutCloseUsage(t *testing.T) string {
	t.Helper()
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			switch ev["type"] {
			case "session.start":
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
			case "session.close":
				_ = conn.WriteJSON(map[string]any{"type": "session.closed", "reason": "close_requested"})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// newFakeGPTLiveServerEndingItsOwnSession answers session.start with
// session.started and then, unprompted, ends the session itself: a
// session.closed nobody asked for, followed by dropping the connection. That
// is the SERVICE-initiated end — its own duration cap, a quota trip, an
// operator action — and it is the one path that emits no gptlive.Error at all,
// because classify sees closedEv already closed and run() returns nil.
func newFakeGPTLiveServerEndingItsOwnSession(t *testing.T) string {
	t.Helper()
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			if ev["type"] == "session.start" {
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
				_ = conn.WriteJSON(map[string]any{
					"type": "session.closed", "reason": "service_ended", "usage": map[string]any{"seconds": 11},
				})
				return // drop the connection, the way a service that is done with you does
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestServiceInitiatedSessionEndLeavesTheRoom covers the final whole-branch
// review's Important 1. onEvent's Error{Recoverable:false} case was the only
// thing that called rs.Leave(), but run() emits no Error whatsoever when the
// SERVICE ends the session on its own: classify sees closedEv already closed
// and returns nil, run returns, the event and audio channels close, and
// pumpEvents/pumpAudioOut simply observed their channels close and returned.
// Nothing published a state change, disconnected the room, or persisted
// anything — the child sat in a connected room with an agent that would never
// speak again.
//
// rs.ctx being cancelled is the observable: RoomSession.leave() cancels it
// first thing, so its Done channel closing proves Leave actually ran.
func TestServiceInitiatedSessionEndLeavesTheRoom(t *testing.T) {
	rs, err := NewRoomSession(RoomSessionConfig{
		RoomInfo:  &lkproto.Room{Name: "room-service-end"},
		ServerURL: "ws://localhost:7880",
		GPTLive:   &GPTLiveSessionSpec{SampleRate: 24000},
	})
	if err != nil {
		t.Fatalf("NewRoomSession() error = %v", err)
	}
	// Join is what normally creates rs.ctx/rs.cancel, and it needs a real
	// LiveKit server; stand them up directly so leave()'s very first act —
	// cancelling rs.ctx — is observable without one.
	rs.ctx, rs.cancel = context.WithCancel(context.Background())

	url := newFakeGPTLiveServerEndingItsOwnSession(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	p := rs.gptlive
	if err := p.finishStart(context.Background(), sess); err != nil {
		t.Fatalf("finishStart() error = %v", err)
	}

	select {
	case <-rs.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the service ended the session and nothing tore the room session down: rs.ctx was never " +
			"cancelled, so the child would still be sitting in a connected room with a silent agent")
	}
}

// TestPipelineCloseDoesNotLeaveTheRoomItself is Important 1's other half: an
// ordinary teardown (leave() -> Close) also closes the event channel, and
// pumpEvents must NOT read that as a service-initiated end and schedule a
// second, re-entrant Leave. p.closing is the discriminator.
func TestPipelineCloseDoesNotLeaveTheRoomItself(t *testing.T) {
	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000})
	if !p.shouldLeaveOnEventsClose() {
		t.Fatal("a pipeline nobody has closed must treat an events-channel close as a service-initiated end")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	p.Close(closeCtx) // p.sess is nil, so this only records that a close was requested
	cancel()
	if p.shouldLeaveOnEventsClose() {
		t.Fatal("after Close, the events-channel close is OUR doing: leave() is already driving the teardown " +
			"and calling Leave from pumpEvents would be re-entrant")
	}
}

// TestGreetBeforeDialIsReplayedByFinishStart covers Important 4. Join connects
// the room and publishes the local track BEFORE it calls Start, so the
// gateway's "ready_for_greeting" data message can land across the entire dial
// window (a 10s timeout with 3 retries at 2s). Greet used to return silently
// when p.sess was still nil, recording nothing, so the child then waited out
// the 3-second fallback timer — which does not even start until finishStart
// spawns it.
func TestGreetBeforeDialIsReplayedByFinishStart(t *testing.T) {
	url := newMinimalFakeGPTLiveServer(t)
	sess, err := gptlive.Dial(context.Background(), gptlive.Config{APIKey: "test", BaseURL: url})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer sess.Close(context.Background())

	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000, Persona: GPTLivePersona{Greeting: "hi"}})
	p.Greet() // the ready_for_greeting that arrives mid-dial
	p.mu.Lock()
	pending, greeted := p.pendingGreet, p.greeted
	p.mu.Unlock()
	if !pending || greeted {
		t.Fatalf("a Greet before the dial must be remembered, not performed or dropped: pendingGreet=%v greeted=%v", pending, greeted)
	}

	if err := p.finishStart(context.Background(), sess); err != nil {
		t.Fatalf("finishStart() error = %v", err)
	}
	p.mu.Lock()
	greeted = p.greeted
	p.mu.Unlock()
	if !greeted {
		t.Fatal("finishStart must replay the pending greeting immediately, not leave the child waiting " +
			"for the 3-second fallback timer it only just started")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	p.Close(closeCtx)
	cancel()
}

// TestQuizDirectivesPushedToTheVoiceModelDeclareTheySupersede covers Important
// 3. AppendInstructions only ever appends and the protocol offers no replace,
// so a ten-question session with misses accumulates 10-30 live "## This
// Question" blocks, each naming a different question id and all of them
// reading as current. Every directive therefore carries an explicit supersedes
// line, so only the last one is authoritative.
func TestQuizDirectivesPushedToTheVoiceModelDeclareTheySupersede(t *testing.T) {
	var mu sync.Mutex
	var appended []string
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			switch ev["type"] {
			case "session.start":
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
			case "session.instructions.append":
				content, _ := ev["content"].(string)
				mu.Lock()
				appended = append(appended, content)
				mu.Unlock()
			case "session.close":
				_ = conn.WriteJSON(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": 1}})
			}
		}
	}))
	defer srv.Close()

	sess, err := gptlive.Dial(context.Background(), gptlive.Config{
		APIKey: "test", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}

	tracker := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{SampleRate: 24000, Quiz: tracker})
	if err := p.finishStart(context.Background(), sess); err != nil {
		t.Fatalf("finishStart() error = %v", err)
	}

	if _, err := tracker.Score("11", "miss", "six"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Score("11", "correct", "eight"); err != nil {
		t.Fatal(err)
	}

	// The pushes travel over the websocket, so give the server a moment to see
	// them before closing; Close itself also flushes the outgoing queue.
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	p.Close(closeCtx)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	// The greeting (Persona.Greeting is empty here, so none is sent) aside,
	// every instructions.append on this session is a Door directive.
	var directives []string
	for _, c := range appended {
		if strings.Contains(c, "## This Question") {
			directives = append(directives, c)
		}
	}
	if len(directives) != 2 {
		t.Fatalf("expected one Door directive pushed per scored turn, got %d: %q", len(directives), directives)
	}
	for i, d := range directives {
		if !strings.HasPrefix(d, gptLiveDirectiveSupersedes) {
			t.Errorf("directive %d reaches the voice model without declaring that it supersedes the earlier "+
				"ones, so all of them keep reading as current: %q", i, d)
		}
	}
}

// TestGreetSendsShortNudgeNotFullGreetingPrompt covers the production fix directly at
// Greet's call site: a real session logged "gptlive: invalid_request_error
// (invalid_value): Context append text must not exceed 500 tokens" because Greet used to
// append "<instruction prefix> + Persona.Greeting" as commentary, and a 2455-byte
// manager-supplied greeting prompt blew through the service's per-append cap — the append
// was rejected outright and the child was never greeted. The full guidance now lives in
// Persona.Voice instead (sent once, uncapped, at session start; see
// TestBuildGPTLivePersonaVoiceCarriesFullGreetingGuidance), so Greet's own commentary must
// be short regardless of how long the greeting prompt is, and must never embed it.
func TestGreetSendsShortNudgeNotFullGreetingPrompt(t *testing.T) {
	var mu sync.Mutex
	var commentary []string
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				continue
			}
			switch ev["type"] {
			case "session.start":
				_ = conn.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "test"}})
			case "session.commentary.append":
				content, _ := ev["content"].(string)
				mu.Lock()
				commentary = append(commentary, content)
				mu.Unlock()
			case "session.close":
				_ = conn.WriteJSON(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": 1}})
			}
		}
	}))
	defer srv.Close()

	sess, err := gptlive.Dial(context.Background(), gptlive.Config{
		APIKey: "test", BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}

	// A long greeting prompt, sized like the one that actually blew the cap in production.
	longGreeting := strings.Repeat("Ask the child about their day at school and what they had for lunch. ", 40)
	if len(longGreeting) < 2000 {
		t.Fatalf("test fixture too small to stand in for the 2455-byte production prompt: %d bytes", len(longGreeting))
	}
	p := newGPTLivePipeline(&RoomSession{}, GPTLiveSessionSpec{
		SampleRate: 24000,
		Persona:    GPTLivePersona{Greeting: longGreeting},
	})
	if err := p.finishStart(context.Background(), sess); err != nil {
		t.Fatalf("finishStart() error = %v", err)
	}
	p.Greet()

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	p.Close(closeCtx)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(commentary) != 1 {
		t.Fatalf("expected exactly one commentary append from Greet, got %d: %q", len(commentary), commentary)
	}
	got := commentary[0]
	if strings.Contains(got, longGreeting) {
		t.Fatalf("Greet's commentary embeds the full greeting prompt; it must only nudge the model toward the "+
			"guidance already sent in Voice: %q", got)
	}
	// 500 tokens at ~4 chars/token is roughly 2000 characters; the nudge must sit
	// comfortably under that regardless of how long any character's greeting prompt is.
	if len(got) > 300 {
		t.Errorf("Greet's commentary is %d chars, want a short nudge well under the service's per-append cap: %q", len(got), got)
	}
}

// TestWaitForEventsDrainNoOpWhenPumpsNeverStarted covers the other half of
// WaitForEventsDrain's contract: a pipeline whose pumps were never spawned —
// exercised here with the same bare &gptLivePipeline{} the pure-logic tests
// above use, standing in for both a Start dial failure and finishStart's own
// self-close branch — must return immediately rather than blocking on a
// channel (eventsDone) nothing will ever close.
func TestWaitForEventsDrainNoOpWhenPumpsNeverStarted(t *testing.T) {
	p := &gptLivePipeline{}
	done := make(chan struct{})
	go func() {
		p.WaitForEventsDrain(5 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitForEventsDrain blocked even though eventsPumpStarted was never set")
	}
}
