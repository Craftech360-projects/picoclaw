package livekit

import (
	"context"
	"sync"
	"testing"
	"time"

	livekitproto "github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

// fakePTTStream is stt.TranscriptionStream plus the two ptt-batch-stt/002
// extension points (ResetBuffer, SetEmptyResultHandler), so the wiring added
// in this ticket can be exercised without pkg/voice/stt's real HTTP adapter.
type fakePTTStream struct {
	mu              sync.Mutex
	resetCalls      int
	cancelCalls     int
	audioSinceReset bool
	emptyResult     func()
}

func (f *fakePTTStream) SendAudio([]byte) error { return nil }
func (f *fakePTTStream) Results() <-chan stt.TranscriptEvent {
	return make(chan stt.TranscriptEvent)
}
func (f *fakePTTStream) Finalize() error { return nil }
func (f *fakePTTStream) Close() error    { return nil }

func (f *fakePTTStream) ResetBuffer() {
	f.mu.Lock()
	f.resetCalls++
	f.audioSinceReset = false
	f.mu.Unlock()
}

// CancelTurn discards like ResetBuffer and additionally marks the turn as
// deliberately abandoned. Counted separately because the whole point of the
// split is that the cancel path must reach THIS and the press path must not —
// without it here, cancelPTTTurn's type assertion falls through to ResetBuffer
// and every test below passes while proving nothing.
func (f *fakePTTStream) CancelTurn() {
	f.mu.Lock()
	f.resetCalls++
	f.cancelCalls++
	f.audioSinceReset = false
	f.mu.Unlock()
}

func (f *fakePTTStream) cancels() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCalls
}

func (f *fakePTTStream) SetEmptyResultHandler(fn func()) {
	f.mu.Lock()
	f.emptyResult = fn
	f.mu.Unlock()
}

func (f *fakePTTStream) resets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetCalls
}

func recvTurnEvent(t *testing.T, ch chan interface{}, timeout time.Duration) (vad.VADEvent, bool) {
	t.Helper()
	select {
	case v := <-ch:
		evt, ok := v.(vad.VADEvent)
		if !ok {
			t.Fatalf("turnEvents carried %T, want vad.VADEvent", v)
		}
		return evt, true
	case <-time.After(timeout):
		return vad.VADEvent{}, false
	}
}

func TestIsPTTDrivenProvider(t *testing.T) {
	for _, tt := range []struct {
		name string
		want bool
	}{
		{"sarvam_rest", true},
		{"sarvam", false},
		{"deepgram", false},
		{"", false},
		{"  sarvam_rest  ", true},
	} {
		if got := isPTTDrivenProvider(tt.name); got != tt.want {
			t.Errorf("isPTTDrivenProvider(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// fakePTTProvider only needs Name() — handleDataMessage's gate is providers
// named "sarvam_rest".
type fakePTTProvider struct{ name string }

func (p *fakePTTProvider) Name() string                     { return p.name }
func (p *fakePTTProvider) Capabilities() stt.ProviderCapabilities { return stt.ProviderCapabilities{} }
func (p *fakePTTProvider) OpenStream(context.Context, stt.StreamOptions) (stt.TranscriptionStream, error) {
	return nil, nil
}

func newPTTTestSession(t *testing.T, providerName string) (*RoomSession, *fakePTTStream) {
	t.Helper()
	stream := &fakePTTStream{}
	ps := &ParticipantState{
		identity:   "device-a",
		sessionKey: "livekit:device:a",
		sttStream:  stream,
		turnEvents: make(chan interface{}, 10),
	}
	rs := &RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		stt:         &fakePTTProvider{name: providerName},
		participant: ps,
	}
	rs.activePipeline = NewAudioPipeline(rs, nil, &capturingTTSProvider{}, nil)
	return rs, stream
}

// press builds the real gateway payload shape: the gateway forwards the
// firmware's listen state verbatim alongside the derived action.
func pressMsg() []byte {
	return []byte(`{"type":"ptt_event","action":"press","state":"start","mode":"manual"}`)
}

func releaseMsg() []byte {
	return []byte(`{"type":"ptt_event","action":"release","state":"stop"}`)
}

func TestHandleDataMessagePTTPressResetsAndInjectsSpeechStart(t *testing.T) {
	rs, stream := newPTTTestSession(t, "sarvam_rest")
	rs.handleDataMessage(pressMsg())

	if stream.resets() != 1 {
		t.Fatalf("ResetBuffer calls = %d, want 1", stream.resets())
	}
	evt, ok := recvTurnEvent(t, rs.participant.turnEvents, time.Second)
	if !ok {
		t.Fatal("no turn event injected for press")
	}
	if !evt.SpeechStart || evt.SpeechEnd {
		t.Fatalf("event = %+v, want SpeechStart only", evt)
	}
}

// Cancel Turn: release must reset the buffer BEFORE the synthetic SpeechEnd
// reaches RunInbound, so Finalize sees nothing to transcribe. Ticket 002
// already proves an empty-buffer Finalize is a silent no-op; this test only
// proves this ticket's half — the ordering and that the event is injected.
func TestHandleDataMessagePTTReleaseCancelsTurn(t *testing.T) {
	rs, stream := newPTTTestSession(t, "sarvam_rest")
	rs.handleDataMessage(releaseMsg())

	evt, ok := recvTurnEvent(t, rs.participant.turnEvents, 2*time.Second)
	if !ok {
		t.Fatal("no turn event injected for release")
	}
	if !evt.SpeechEnd || evt.SpeechStart {
		t.Fatalf("event = %+v, want SpeechEnd only", evt)
	}
	// Twice: once on arrival, once after the grace — audio frames still in
	// flight land between them and must not survive to be transcribed.
	if stream.resets() != 2 {
		t.Fatalf("ResetBuffer calls = %d, want 2 (before and after the grace)", stream.resets())
	}
}

// The buffer must be empty at the moment Cancel Turn's SpeechEnd reaches
// RunInbound, even if audio arrives during the grace window — otherwise
// Finalize transcribes the tail and the LLM answers a cancelled question.
func TestHandleDataMessagePTTReleaseWipesAudioArrivingDuringGrace(t *testing.T) {
	rs, stream := newPTTTestSession(t, "sarvam_rest")
	rs.handleDataMessage(releaseMsg())

	// A trailing frame lands right after the first reset.
	stream.mu.Lock()
	stream.audioSinceReset = true
	stream.mu.Unlock()

	if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 2*time.Second); !ok {
		t.Fatal("release never injected SpeechEnd")
	}
	stream.mu.Lock()
	dirty := stream.audioSinceReset
	stream.mu.Unlock()
	if dirty {
		t.Fatal("audio that arrived during the grace survived to the turn boundary")
	}
}

func TestHandleDataMessageSpeechEndInjectsAfterGrace(t *testing.T) {
	rs, _ := newPTTTestSession(t, "sarvam_rest")
	start := time.Now()
	rs.handleDataMessage([]byte(`{"type":"speech_end"}`))

	// Must not be immediate — in-flight audio frames need the grace window.
	if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 50*time.Millisecond); ok {
		t.Fatal("speech_end injected SpeechEnd before the grace period elapsed")
	}
	evt, ok := recvTurnEvent(t, rs.participant.turnEvents, 2*time.Second)
	if !ok {
		t.Fatal("speech_end never injected a turn event")
	}
	if !evt.SpeechEnd {
		t.Fatalf("event = %+v, want SpeechEnd", evt)
	}
	if elapsed := time.Since(start); elapsed < pttSpeechEndGrace() {
		t.Fatalf("event arrived after %v, want at least the %v grace", elapsed, pttSpeechEndGrace())
	}
}

// The gateway derives action from the firmware's listen state, collapsing
// every non-"start" state into "release" — including "detect", which the
// client sends to kick the greeting and on its stalled-TTS retry. Only a real
// "stop" is a Cancel Turn; anything else must leave a live turn alone.
func TestHandleDataMessagePTTReleaseIgnoresNonStopStates(t *testing.T) {
	for _, state := range []string{"detect", "", "listening"} {
		rs, stream := newPTTTestSession(t, "sarvam_rest")
		msg := `{"type":"ptt_event","action":"release","state":"` + state + `"}`
		rs.handleDataMessage([]byte(msg))

		if stream.resets() != 0 {
			t.Errorf("state %q wiped the buffer; only \"stop\" is a Cancel Turn", state)
		}
		if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 200*time.Millisecond); ok {
			t.Errorf("state %q injected SpeechEnd; only \"stop\" is a Cancel Turn", state)
		}
	}
}

// Firmware sends listen/start and speech_end unconditionally in Manual Talk —
// a session running any other provider must ignore them entirely.
func TestHandleDataMessagePTTIgnoredForNonPTTProvider(t *testing.T) {
	for _, msg := range []string{
		string(pressMsg()),
		string(releaseMsg()),
		`{"type":"speech_end"}`,
	} {
		rs, stream := newPTTTestSession(t, "sarvam")
		rs.handleDataMessage([]byte(msg))
		if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 300*time.Millisecond); ok {
			t.Errorf("message %s injected a turn event for a non-PTT provider", msg)
		}
		if stream.resets() != 0 {
			t.Errorf("message %s called ResetBuffer for a non-PTT provider", msg)
		}
	}
}

// Data messages can arrive before the track subscribes and rs.participant is
// set — must not panic.
func TestHandleDataMessagePTTNoParticipantYet(t *testing.T) {
	rs := &RoomSession{
		roomInfo: &livekitproto.Room{Name: "room-a"},
		stt:      &fakePTTProvider{name: "sarvam_rest"},
	}
	for _, msg := range []string{
		string(pressMsg()),
		string(releaseMsg()),
		`{"type":"speech_end"}`,
	} {
		rs.handleDataMessage([]byte(msg))
	}
}

// A tap can land after rs.participant is published but before the STT stream
// and turn channel are wired (handleTrackSubscribed does the latter only after
// opening the provider stream). Must not panic, and must not vanish silently.
func TestHandleDataMessagePTTHalfInitializedParticipant(t *testing.T) {
	rs := &RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		stt:         &fakePTTProvider{name: "sarvam_rest"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}
	rs.activePipeline = NewAudioPipeline(rs, nil, &capturingTTSProvider{}, nil)
	for _, msg := range []string{string(pressMsg()), string(releaseMsg()), `{"type":"speech_end"}`} {
		rs.handleDataMessage([]byte(msg))
	}
}

func TestWireEmptyResultAnnouncementRegistersHandler(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)
	pipeline.notePTTPress()
	pipeline.noteEndTurn()

	stream := &fakePTTStream{}
	wireEmptyResultAnnouncement(stream, pipeline)

	stream.mu.Lock()
	handler := stream.emptyResult
	stream.mu.Unlock()
	if handler == nil {
		t.Fatal("SetEmptyResultHandler was never called")
	}
	handler()

	// Speaking is spawned, not inline (the provider's Close() waits on the
	// goroutine this fires from), so the phrase lands shortly after.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ttsProvider.LastText() == pipeline.didntHearFallbackPhrase() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("spoken text = %q, want the didn't-hear phrase %q", ttsProvider.LastText(), pipeline.didntHearFallbackPhrase())
}

// The handler must not block its caller: the provider fires it from the
// goroutine Close() waits on, while teardown holds the very lock speaking
// needs. Returning promptly is what keeps that from deadlocking.
func TestWireEmptyResultAnnouncementDoesNotBlockCaller(t *testing.T) {
	pipeline, _ := newFallbackTestPipeline(t)
	pipeline.notePTTPress()
	pipeline.noteEndTurn()

	stream := &fakePTTStream{}
	wireEmptyResultAnnouncement(stream, pipeline)
	stream.mu.Lock()
	handler := stream.emptyResult
	stream.mu.Unlock()

	done := make(chan struct{})
	go func() {
		handler()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("empty-result handler blocked its caller")
	}
}

// A stream with no empty-result support (every other provider) must be left
// alone — no panic, nothing registered.
func TestWireEmptyResultAnnouncementNoopForPlainStream(t *testing.T) {
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, nil, &capturingTTSProvider{}, nil)
	wireEmptyResultAnnouncement(&fakeTranscriptionStream{}, pipeline)
}

func newFallbackTestPipeline(t *testing.T) (*AudioPipeline, *capturingTTSProvider) {
	t.Helper()
	localTrack, err := lkmedia.NewPCMLocalTrack(24000, 1, protoLogger.GetLogger())
	if err != nil {
		t.Fatalf("NewPCMLocalTrack() error = %v", err)
	}
	t.Cleanup(func() { localTrack.Close() })

	ttsProvider := &capturingTTSProvider{}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
		localTrack:  localTrack,
		sampleRate:  24000,
	}, nil, ttsProvider, nil)
	return pipeline, ttsProvider
}

func TestSpeakDidntHearFallbackSpeaksAfterEndTurn(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.notePTTPress()
	pipeline.noteEndTurn()
	pipeline.speakDidntHearFallback()

	if got, want := ttsProvider.LastText(), pipeline.didntHearFallbackPhrase(); got != want {
		t.Fatalf("spoken text = %q, want %q", got, want)
	}
}

// "Cancel Turn must never announce" — even when trailing audio frames slip in
// after the buffer reset and produce a real (empty-transcript) REST call.
func TestSpeakDidntHearFallbackSilentAfterCancelTurn(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.notePTTPress()
	pipeline.noteCancelTurn()
	pipeline.speakDidntHearFallback()

	if got := ttsProvider.LastText(); got != "" {
		t.Fatalf("announced %q on a cancelled turn, want silence", got)
	}
}

// A slow REST call can return after the child already pressed again; that
// stale "no transcript" belongs to an utterance they have moved past.
func TestSpeakDidntHearFallbackSilentForStaleGeneration(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.notePTTPress()
	pipeline.noteEndTurn() // turn 1 finalized, REST call in flight
	pipeline.notePTTPress() // child presses again before it returns

	pipeline.speakDidntHearFallback()

	if got := ttsProvider.LastText(); got != "" {
		t.Fatalf("announced %q for a superseded turn, want silence", got)
	}
}

// speech_end with no preceding press (the tap that opens a session) is still
// a legitimate End Turn.
func TestSpeakDidntHearFallbackSpeaksForEndTurnWithoutPress(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.noteEndTurn()
	pipeline.speakDidntHearFallback()

	if got, want := ttsProvider.LastText(), pipeline.didntHearFallbackPhrase(); got != want {
		t.Fatalf("spoken text = %q, want %q", got, want)
	}
}

// Nothing has happened yet: an empty result with no turn context must not
// announce out of nowhere.
func TestSpeakDidntHearFallbackSilentWithoutAnyTurn(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.speakDidntHearFallback()

	if got := ttsProvider.LastText(); got != "" {
		t.Fatalf("announced %q with no turn in progress, want silence", got)
	}
}

// A real reply must never be talked over by a stale empty-result callback
// (issue 002: the callback can arrive after a fresh press already got a
// legitimate answer moving).
func TestSpeakDidntHearFallbackSuppressedWhileTurnActive(t *testing.T) {
	pipeline, ttsProvider := newFallbackTestPipeline(t)

	pipeline.notePTTPress()
	pipeline.noteEndTurn()
	turn := pipeline.turns.Start(context.Background(), "test_turn")
	defer pipeline.turns.Finish(turn)

	pipeline.speakDidntHearFallback()

	if got := ttsProvider.LastText(); got != "" {
		t.Fatalf("spoke %q while a turn was active, want silence", got)
	}
}

// The two paths that empty the buffer must stay distinguishable all the way
// down to the provider, because an empty Finalize now ends differently for
// each: a cancelled turn stays silent, a turn that captured nothing is
// answered with "I didn't hear you".
//
// Asserted through the real handleDataMessage rather than by calling
// cancelPTTTurn directly: the wiring is a type assertion, so a provider that
// grew CancelTurn while the call site still said ResetBuffer would look
// perfectly correct in isolation and be silently wrong here.
func TestPTTCancelUsesCancelTurnAndPressDoesNot(t *testing.T) {
	t.Run("press resets without cancelling", func(t *testing.T) {
		rs, stream := newPTTTestSession(t, "sarvam_rest")
		rs.handleDataMessage(pressMsg())

		if stream.resets() != 1 {
			t.Errorf("ResetBuffer calls = %d, want 1", stream.resets())
		}
		// A press that suppressed the announcement would silence every empty
		// turn that followed it — which is the bug, not the fix.
		if stream.cancels() != 0 {
			t.Errorf("CancelTurn calls = %d on a press, want 0", stream.cancels())
		}
	})

	t.Run("release cancels on both wipes", func(t *testing.T) {
		rs, stream := newPTTTestSession(t, "sarvam_rest")
		rs.handleDataMessage(releaseMsg())

		if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 2*time.Second); !ok {
			t.Fatal("no turn event injected for release")
		}
		// Both wipes must cancel, not just the first: the flag is one-shot, so
		// if the post-grace wipe only reset, the tail frames it exists to
		// discard would be announced instead of silently dropped.
		if stream.cancels() != 2 {
			t.Errorf("CancelTurn calls = %d, want 2 (arrival and post-grace)", stream.cancels())
		}
	})

	t.Run("end turn never cancels", func(t *testing.T) {
		rs, stream := newPTTTestSession(t, "sarvam_rest")
		rs.handleDataMessage([]byte(`{"type":"speech_end"}`))

		if _, ok := recvTurnEvent(t, rs.participant.turnEvents, 2*time.Second); !ok {
			t.Fatal("no turn event injected for speech_end")
		}
		// End Turn is the path that must reach the child: it neither discards
		// the utterance nor suppresses the empty-tap announcement.
		if stream.cancels() != 0 {
			t.Errorf("CancelTurn calls = %d on End Turn, want 0", stream.cancels())
		}
		if stream.resets() != 0 {
			t.Errorf("ResetBuffer calls = %d on End Turn, want 0", stream.resets())
		}
	})
}
