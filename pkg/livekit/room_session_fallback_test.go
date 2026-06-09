package livekit

import (
	"context"
	"errors"
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
