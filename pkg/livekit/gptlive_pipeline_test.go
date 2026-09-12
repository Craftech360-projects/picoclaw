package livekit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
