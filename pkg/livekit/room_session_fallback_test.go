package livekit

import (
	"context"
	"errors"
	"slices"
	"testing"

	livekitproto "github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
)

func TestRoomSessionSpeaksSTTUnavailableFallback(t *testing.T) {
	localTrack, err := lkmedia.NewPCMLocalTrack(24000, 1, protoLogger.GetLogger())
	if err != nil {
		t.Fatalf("NewPCMLocalTrack error = %v", err)
	}
	defer localTrack.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttsProvider := &capturingTTSProvider{}
	rs := &RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
		localTrack:  localTrack,
		sampleRate:  24000,
		tts:         ttsProvider,
		ctx:         ctx,
	}

	rs.speakSTTUnavailableFallback(errors.New("stt unavailable"))

	if got, want := ttsProvider.LastText(), sttUnavailableFallbackPhrase(); got != want {
		t.Fatalf("STT unavailable fallback TTS text = %q, want %q", got, want)
	}
}

func TestHandleEndPromptInterruptsActivePipeline(t *testing.T) {
	cancelCalls := 0
	participant := &ParticipantState{
		identity:   "device-a",
		sessionKey: "livekit:device:a",
		ttsCancel:  func() { cancelCalls++ },
	}
	rs := &RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: participant,
	}
	pipeline := NewAudioPipeline(rs, nil, nil, nil)
	rs.activePipeline = pipeline
	pipeline.publishAgentState = func(oldState, newState string) {}
	turn := pipeline.startTurn(context.Background(), "test_turn")

	// bridge == nil: the farewell itself is skipped, but the in-flight
	// response must still be interrupted before the session ends.
	rs.handleEndPrompt("bye now")

	if cancelCalls != 1 {
		t.Fatalf("tts cancel calls = %d, want 1", cancelCalls)
	}
	if err := turn.ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn context error = %v, want context.Canceled", err)
	}
}

func TestHandleDataMessageAbortInterruptsActivePipeline(t *testing.T) {
	cancelCalls := 0
	participant := &ParticipantState{
		identity:   "device-a",
		sessionKey: "livekit:device:a",
		ttsCancel:  func() { cancelCalls++ },
	}
	rs := &RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: participant,
	}
	pipeline := NewAudioPipeline(rs, nil, nil, nil)
	rs.activePipeline = pipeline

	var states []string
	pipeline.publishAgentState = func(oldState, newState string) {
		states = append(states, oldState+"->"+newState)
	}
	turn := pipeline.startTurn(context.Background(), "test_turn")

	rs.handleDataMessage([]byte(`{"type":"abort","session_id":"livekit:device:a","source":"mqtt_gateway"}`))

	if cancelCalls != 1 {
		t.Fatalf("tts cancel calls = %d, want 1", cancelCalls)
	}
	if err := turn.ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn context error = %v, want context.Canceled", err)
	}
	if !slices.Contains(states, "speaking->listening") {
		t.Fatalf("state transitions = %v, want speaking->listening", states)
	}
	participant.mu.Lock()
	gotReason := participant.ttsCancelReason
	hasCancel := participant.ttsCancel != nil
	participant.mu.Unlock()
	if gotReason != "mqtt_abort" {
		t.Fatalf("tts cancel reason = %q, want mqtt_abort", gotReason)
	}
	if hasCancel {
		t.Fatal("tts cancel callback was not cleared")
	}
}
