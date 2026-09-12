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

func TestPipelineVoiceSecondsTracksLatestUsageNotSum(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.VoiceUsage{Seconds: 5})
	p.onEvent(gptlive.VoiceUsage{Seconds: 12})
	// VoiceUsage.Seconds is the session's running total to date, not a delta —
	// summing successive updates would overcount, so the latest value must win.
	if got := p.VoiceSeconds(); got != 12 {
		t.Errorf("VoiceSeconds() = %v, want 12 (latest, not summed)", got)
	}
	p.onEvent(gptlive.Closed{Reason: "close_requested", VoiceSeconds: 20})
	if got := p.VoiceSeconds(); got != 20 {
		t.Errorf("VoiceSeconds() after Closed = %v, want 20", got)
	}
}

func TestPipelineBackendTokensSumsAcrossResponses(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.BackendUsage{Total: 100})
	p.onEvent(gptlive.BackendUsage{Total: 50})
	// BackendUsage.Total is per completed backend response (one per delegation),
	// so the session total is the sum across every response.
	if got := p.BackendTokens(); got != 150 {
		t.Errorf("BackendTokens() = %v, want 150 (summed across responses)", got)
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
func TestPublishAgentStateOnRoomMatchesPublishAgentStateForNilRoom(t *testing.T) {
	if err := publishAgentStateOnRoom(nil, "listening", "speaking"); err == nil {
		t.Error("expected an error for a nil room, matching RoomSession.PublishAgentState's own nil-room behavior")
	}
}
