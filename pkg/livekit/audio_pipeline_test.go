package livekit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	livekitproto "github.com/livekit/protocol/livekit"
	protoLogger "github.com/livekit/protocol/logger"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

func TestSanitizeVoiceTextForTTSDropsProviderChannelMarkers(t *testing.T) {
	got := sanitizeVoiceTextForTTS("<|channel>thought <channel|>I'm sorry, I had trouble.")
	want := "I'm sorry, I had trouble."
	if got != want {
		t.Fatalf("sanitizeVoiceTextForTTS() = %q, want %q", got, want)
	}
}

func TestSanitizeVoiceTextForTTSDropsReasoningBlocks(t *testing.T) {
	got := sanitizeVoiceTextForTTS("<think>I should think first</think>Here is the answer.")
	want := "Here is the answer."
	if got != want {
		t.Fatalf("sanitizeVoiceTextForTTS() = %q, want %q", got, want)
	}
}

func TestSanitizeVoiceTextForTTSDropsThoughtBlocks(t *testing.T) {
	got := sanitizeVoiceTextForTTS("<thought>internal plan</thought>Sure, here is a short story.")
	want := "Sure, here is a short story."
	if got != want {
		t.Fatalf("sanitizeVoiceTextForTTS() = %q, want %q", got, want)
	}
}

func TestShouldHoldShortUtterance(t *testing.T) {
	if !shouldHoldShortUtterance("Hello") {
		t.Fatal("expected Hello to be held")
	}
	if !shouldHoldShortUtterance("okay.") {
		t.Fatal("expected okay to be held")
	}
	if shouldHoldShortUtterance("Can you tell me a story?") {
		t.Fatal("did not expect full sentence to be held")
	}
}

func TestShouldSuppressDuplicateShortBargeInTranscript(t *testing.T) {
	now := time.Now()
	if !shouldSuppressBargeInTranscript("Hello.", "hello", now.Add(-500*time.Millisecond), now, "") {
		t.Fatal("expected duplicate short barge-in transcript to be suppressed")
	}
	if shouldSuppressBargeInTranscript("Please tell me the weather", "hello", now.Add(-500*time.Millisecond), now, "") {
		t.Fatal("did not expect full utterance to be suppressed")
	}
	if shouldSuppressBargeInTranscript("Hello", "hello", now.Add(-2*time.Second), now, "") {
		t.Fatal("did not expect old duplicate short utterance to be suppressed")
	}
}

func TestShouldSuppressPendingShortBargeInTranscript(t *testing.T) {
	if !shouldSuppressBargeInTranscript("Okay.", "", time.Time{}, time.Now(), "okay") {
		t.Fatal("expected duplicate pending short utterance to be suppressed")
	}
	if shouldSuppressBargeInTranscript("Okay, tell me more", "", time.Time{}, time.Now(), "okay") {
		t.Fatal("did not expect expanded utterance to be suppressed")
	}
}

func TestTTSAudioTailSampleCountUsesSessionSampleRate(t *testing.T) {
	got := ttsAudioTailSampleCount(24000, liveKitTTSAudioTailMs)
	if got != 6000 {
		t.Fatalf("tail sample count = %d, want 6000", got)
	}
}

func TestTTSAudioTailSampleCountDefaultsToTwentyFourKilohertz(t *testing.T) {
	got := ttsAudioTailSampleCount(0, liveKitTTSAudioTailMs)
	if got != 6000 {
		t.Fatalf("default tail sample count = %d, want 6000", got)
	}
}

func TestSynthesizeAndPlayLogsTTSProviderType(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "livekit-tts.log")
	if err := logger.EnableFileLogging(logPath); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	t.Cleanup(logger.DisableFileLogging)

	localTrack, err := lkmedia.NewPCMLocalTrack(24000, 1, protoLogger.GetLogger())
	if err != nil {
		t.Fatalf("NewPCMLocalTrack error = %v", err)
	}
	defer localTrack.Close()

	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo: &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{
			identity:   "device-a",
			sessionKey: "livekit:device:a",
		},
		localTrack: localTrack,
		sampleRate: 24000,
	}, nil, &capturingTTSProvider{}, nil)

	pipeline.synthesizeAndPlay(context.Background(), "hello")
	logger.DisableFileLogging()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logs := string(data)
	if !strings.Contains(logs, `"message":"Synthesizing audio chunk"`) {
		t.Fatalf("logs missing synthesis marker:\n%s", logs)
	}
	if !strings.Contains(logs, `"tts_provider_type":"*livekit.capturingTTSProvider"`) {
		t.Fatalf("logs missing TTS provider type:\n%s", logs)
	}
}

func TestFinalTransportTailIsReducedForESPBuffering(t *testing.T) {
	if liveKitFinalTransportTailMs != 250 {
		t.Fatalf("final transport tail = %dms, want 250ms", liveKitFinalTransportTailMs)
	}
}

func TestRunInboundSuppressesDuplicateSTTSpeechEndAfterVADFlush(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 4)
	vadEvents := make(chan interface{}, 1)
	stream := &fakeTranscriptionStream{results: results}
	provider := &countingStreamingProvider{calls: make(chan string, 4)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "hello there", IsFinal: true}

	select {
	case got := <-provider.calls:
		if got != "hello there" {
			t.Fatalf("first agent call text = %q, want hello there", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first agent call")
	}

	results <- stt.TranscriptEvent{Text: "hello there", IsFinal: true, SpeechEnd: true}
	close(results)

	select {
	case got := <-provider.calls:
		t.Fatalf("duplicate agent call for same utterance: %q", got)
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}

func TestRunInboundAllowsSameTextAfterNewVADSpeechStart(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 4)
	vadEvents := make(chan interface{}, 4)
	stream := &fakeTranscriptionStream{results: results}
	provider := &countingStreamingProvider{calls: make(chan string, 4)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "hello there", IsFinal: true}
	expectProviderCall(t, provider.calls, "hello there")

	vadEvents <- vad.VADEvent{SpeechStart: true}
	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "hello there", IsFinal: true}
	expectProviderCall(t, provider.calls, "hello there")

	close(results)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}

func TestRunInboundMergesRepeatedFinalTranscriptChunks(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 4)
	vadEvents := make(chan interface{}, 1)
	stream := &fakeTranscriptionStream{results: results}
	provider := &countingStreamingProvider{calls: make(chan string, 4)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	results <- stt.TranscriptEvent{Text: "No, what about singing a song?", IsFinal: true}
	results <- stt.TranscriptEvent{Text: "No, what about singing a song?", IsFinal: true, SpeechEnd: true}
	expectProviderCall(t, provider.calls, "No, what about singing a song?")
	close(results)

	select {
	case got := <-provider.calls:
		t.Fatalf("unexpected duplicate agent call: %q", got)
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}

func TestRunInboundDoesNotCancelTTSOnVADStartWithoutTranscript(t *testing.T) {
	results := make(chan stt.TranscriptEvent)
	vadEvents := make(chan interface{})
	stream := &fakeTranscriptionStream{results: results}
	cancelled := false
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo: &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{
			identity:   "device-a",
			sessionKey: "livekit:device:a",
			ttsCancel:  func() { cancelled = true },
		},
	}, nil, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechStart: true, Probability: 0.80}
	vadEvents <- vad.VADEvent{SpeechEnd: true, Probability: 0.30}
	close(results)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
	if cancelled {
		t.Fatal("TTS was cancelled on VAD-only speech with no transcript")
	}
}

func TestRunInboundCancelsTTSWhenTranscriptArrivesAfterVADStart(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 1)
	vadEvents := make(chan interface{})
	stream := &fakeTranscriptionStream{results: results}
	cancelled := false
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo: &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{
			identity:   "device-a",
			sessionKey: "livekit:device:a",
			ttsCancel:  func() { cancelled = true },
		},
	}, nil, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechStart: true, Probability: 0.80}
	results <- stt.TranscriptEvent{Text: "space", IsFinal: false}
	close(results)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
	if !cancelled {
		t.Fatal("TTS was not cancelled after transcript text arrived")
	}
}

func TestRunInboundSuppressesRepeatedShortBargeInTranscript(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 2)
	vadEvents := make(chan interface{})
	stream := &fakeTranscriptionStream{results: results}
	var cancelCount atomic.Int32
	participant := &ParticipantState{
		identity:   "device-a",
		sessionKey: "livekit:device:a",
		ttsCancel:  func() { cancelCount.Add(1) },
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: participant,
	}, nil, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechStart: true, Probability: 0.80}
	results <- stt.TranscriptEvent{Text: "Hello", IsFinal: false}
	waitForCancelCount(t, &cancelCount, 1)

	participant.mu.Lock()
	participant.ttsCancel = func() { cancelCount.Add(1) }
	participant.mu.Unlock()

	vadEvents <- vad.VADEvent{SpeechStart: true, Probability: 0.82}
	results <- stt.TranscriptEvent{Text: "Hello.", IsFinal: false}

	time.Sleep(150 * time.Millisecond)
	if got := cancelCount.Load(); got != 1 {
		t.Fatalf("duplicate short barge-in canceled TTS %d times, want 1", got)
	}

	close(results)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}

func TestRunInboundNewUtteranceCancelsPreviousTurn(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 8)
	vadEvents := make(chan interface{}, 8)
	provider := newBlockingStreamingProvider()
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, &fakeTranscriptionStream{results: results})
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "first question", IsFinal: true, SpeechEnd: true}
	expectProviderCall(t, provider.started, "first question")

	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "second question", IsFinal: true, SpeechEnd: true}
	expectProviderCall(t, provider.started, "second question")
	expectProviderCall(t, provider.canceled, "first question")

	close(provider.release)
	close(results)

	select {
	case got := <-provider.started:
		t.Fatalf("unexpected extra provider call after superseded turn: %q", got)
	case <-time.After(150 * time.Millisecond):
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}

func TestHandleUtteranceDoesNotRetryCanceledChatStream(t *testing.T) {
	provider := &cancelingStreamingProvider{}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, nil)

	doneCalls := 0
	_, err := pipeline.HandleUtterance(context.Background(), "livekit:device:a", "hello", func() {
		doneCalls++
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleUtterance error = %v, want context.Canceled", err)
	}
	if provider.calls != 1 {
		t.Fatalf("ChatStream calls = %d, want 1", provider.calls)
	}
	if doneCalls != 1 {
		t.Fatalf("onDone calls = %d, want 1", doneCalls)
	}
}

func TestHandleUtteranceForTurnResetsStateOnLLMTimeout(t *testing.T) {
	provider := newBlockingStreamingProvider()
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	localTrack, err := lkmedia.NewPCMLocalTrack(24000, 1, protoLogger.GetLogger())
	if err != nil {
		t.Fatalf("NewPCMLocalTrack error = %v", err)
	}
	defer localTrack.Close()
	ttsProvider := &capturingTTSProvider{}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
		localTrack:  localTrack,
		sampleRate:  24000,
	}, bridge, ttsProvider, nil)
	pipeline.turnTimeout = 25 * time.Millisecond

	var states []string
	pipeline.publishAgentState = func(oldState, newState string) {
		states = append(states, oldState+"->"+newState)
	}

	turn := pipeline.startTurn(context.Background(), "test_timeout")
	_, err = pipeline.HandleUtteranceForTurn(turn, "livekit:device:a", "hello")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HandleUtteranceForTurn error = %v, want deadline exceeded", err)
	}
	if !slices.Contains(states, "listening->thinking") {
		t.Fatalf("state transitions = %v, want listening->thinking", states)
	}
	if !slices.Contains(states, "thinking->listening") {
		t.Fatalf("state transitions = %v, want timeout reset to listening", states)
	}
	if got, want := ttsProvider.LastText(), pipeline.retryFallbackPhrase(); got != want {
		t.Fatalf("timeout fallback TTS text = %q, want %q", got, want)
	}
}

func TestReadAudioChunkReturnsOnContextCancelAndClosesStream(t *testing.T) {
	stream := &blockingAudioStream{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := readAudioChunk(ctx, stream)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readAudioChunk error = %v, want deadline exceeded", err)
	}
	select {
	case <-stream.closed:
	case <-time.After(time.Second):
		t.Fatal("readAudioChunk did not close stream after context cancellation")
	}
}

func TestTriggerGreetingPublishesSpeechCreatedOnFirstChunk(t *testing.T) {
	dynamicGreetingCooldownUntilUnix.Store(0)
	provider := &countingStreamingProvider{calls: make(chan string, 4)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, nil)
	speechCreated := make(chan struct{}, 1)
	pipeline.publishSpeechCreated = func(string) {
		speechCreated <- struct{}{}
	}

	pipeline.TriggerGreeting(context.Background(), "livekit:device:a")

	select {
	case <-speechCreated:
	case <-time.After(time.Second):
		t.Fatal("greeting did not publish speech_created on first assistant chunk")
	}
}

func TestTriggerGreetingPublishesSpeechCreatedOnRateLimitFallback(t *testing.T) {
	dynamicGreetingCooldownUntilUnix.Store(0)
	provider := &rateLimitedStreamingProvider{}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, nil)
	speechCreated := make(chan struct{}, 1)
	pipeline.publishSpeechCreated = func(string) {
		select {
		case speechCreated <- struct{}{}:
		default:
		}
	}

	pipeline.TriggerGreeting(context.Background(), "livekit:device:a")

	select {
	case <-speechCreated:
	case <-time.After(time.Second):
		t.Fatal("rate-limited greeting did not publish speech_created fallback")
	}
}

func TestHandleAsyncEventRateLimitedPublishesFallbackSpeechCreated(t *testing.T) {
	dynamicGreetingCooldownUntilUnix.Store(0)
	provider := &rateLimitedStreamingProvider{}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, nil)
	speechCreated := make(chan struct{}, 1)
	pipeline.publishSpeechCreated = func(string) {
		select {
		case speechCreated <- struct{}{}:
		default:
		}
	}

	pipeline.handleAsyncEvent(AsyncEvent{
		SessionKey: "livekit:device:a",
		ToolName:   "weather",
		Result:     tools.SilentResult("rain expected in 10 minutes"),
	}, false)

	select {
	case <-speechCreated:
	case <-time.After(time.Second):
		t.Fatal("rate-limited spontaneous response did not publish speech_created fallback")
	}
}

func TestHandleAsyncEventCooldownSkipsSpontaneousLLMCall(t *testing.T) {
	dynamicGreetingCooldownUntilUnix.Store(time.Now().Add(time.Minute).Unix())
	defer dynamicGreetingCooldownUntilUnix.Store(0)

	provider := &countingStreamingProvider{calls: make(chan string, 1)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, bridge, nil, nil)
	speechCreated := make(chan struct{}, 1)
	pipeline.publishSpeechCreated = func(string) {
		select {
		case speechCreated <- struct{}{}:
		default:
		}
	}

	pipeline.handleAsyncEvent(AsyncEvent{
		SessionKey: "livekit:device:a",
		ToolName:   "weather",
		Result:     tools.SilentResult("rain expected in 10 minutes"),
	}, false)

	select {
	case <-speechCreated:
	case <-time.After(time.Second):
		t.Fatal("cooldown fallback did not publish speech_created")
	}

	select {
	case got := <-provider.calls:
		t.Fatalf("provider should not be called during cooldown, got %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCancelTTSRecordsBargeInReason(t *testing.T) {
	cancelled := false
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo: &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{
			identity:   "device-a",
			sessionKey: "livekit:device:a",
			ttsCancel:  func() { cancelled = true },
		},
	}, nil, nil, nil)

	pipeline.cancelTTS("vad_speech_start")

	if !cancelled {
		t.Fatal("tts cancel function was not called")
	}
	if got := pipeline.currentTTSCancelReason(); got != "vad_speech_start" {
		t.Fatalf("cancel reason = %q, want vad_speech_start", got)
	}
}

func TestSpeechChunkDeduperSuppressesConsecutiveDuplicateSentences(t *testing.T) {
	deduper := &speechChunkDeduper{}

	if !deduper.ShouldSpeak("Did you know that octopuses have three hearts?") {
		t.Fatal("first sentence was suppressed")
	}
	if deduper.ShouldSpeak("  Did you know that octopuses have three hearts?  ") {
		t.Fatal("consecutive duplicate sentence was not suppressed")
	}
	if !deduper.ShouldSpeak("That is a lot of love!") {
		t.Fatal("different sentence was suppressed")
	}
	if !deduper.ShouldSpeak("Did you know that octopuses have three hearts?") {
		t.Fatal("same sentence after a different sentence should be allowed")
	}
}

func TestSpeechChunkDeduperAllowsShortExpressiveRepeats(t *testing.T) {
	deduper := &speechChunkDeduper{}

	if !deduper.ShouldSpeak("Wah!") {
		t.Fatal("first short interjection was suppressed")
	}
	if !deduper.ShouldSpeak("Wah!") {
		t.Fatal("short expressive repeat should be allowed")
	}
}

type fakeTranscriptionStream struct {
	results chan stt.TranscriptEvent
}

func (f *fakeTranscriptionStream) SendAudio([]byte) error { return nil }
func (f *fakeTranscriptionStream) Results() <-chan stt.TranscriptEvent {
	return f.results
}
func (f *fakeTranscriptionStream) Finalize() error { return nil }
func (f *fakeTranscriptionStream) Close() error    { return nil }

type countingStreamingProvider struct {
	mu    sync.Mutex
	count int
	calls chan string
}

func (p *countingStreamingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "I hear you."}, nil
}

func (p *countingStreamingProvider) ChatStream(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(string),
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.count++
	p.mu.Unlock()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			p.calls <- messages[i].Content
			break
		}
	}
	if onChunk != nil {
		onChunk("I hear you.")
	}
	return &providers.LLMResponse{Content: "I hear you."}, nil
}

func (p *countingStreamingProvider) GetDefaultModel() string { return "test" }

type blockingStreamingProvider struct {
	started  chan string
	canceled chan string
	release  chan struct{}
}

func newBlockingStreamingProvider() *blockingStreamingProvider {
	return &blockingStreamingProvider{
		started:  make(chan string, 4),
		canceled: make(chan string, 4),
		release:  make(chan struct{}),
	}
}

func (p *blockingStreamingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok"}, nil
}

func (p *blockingStreamingProvider) ChatStream(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(string),
) (*providers.LLMResponse, error) {
	userText := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userText = messages[i].Content
			p.started <- userText
			break
		}
	}
	select {
	case <-ctx.Done():
		p.canceled <- userText
		return nil, ctx.Err()
	case <-p.release:
		if onChunk != nil {
			onChunk("finished")
		}
		return &providers.LLMResponse{Content: "finished"}, nil
	}
}

func (p *blockingStreamingProvider) GetDefaultModel() string { return "test" }

type cancelingStreamingProvider struct {
	calls int
}

func (p *cancelingStreamingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	return nil, context.Canceled
}

func (p *cancelingStreamingProvider) ChatStream(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
	func(string),
) (*providers.LLMResponse, error) {
	p.calls++
	return nil, context.Canceled
}

func (p *cancelingStreamingProvider) GetDefaultModel() string { return "test" }

type rateLimitedStreamingProvider struct{}

func (p *rateLimitedStreamingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return nil, errors.New("API request failed: status 429 rate limited")
}

func (p *rateLimitedStreamingProvider) ChatStream(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
	func(string),
) (*providers.LLMResponse, error) {
	return nil, errors.New("API request failed: status 429 rate limited")
}

func (p *rateLimitedStreamingProvider) GetDefaultModel() string { return "test" }

type blockingAudioStream struct {
	once        sync.Once
	readStarted chan struct{}
	closed      chan struct{}
}

func (s *blockingAudioStream) Read() ([]byte, error) {
	s.once.Do(func() {
		close(s.readStarted)
	})
	<-s.closed
	return nil, io.EOF
}

func (s *blockingAudioStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

type capturingTTSProvider struct {
	mu    sync.Mutex
	texts []string
}

func (p *capturingTTSProvider) Synthesize(ctx context.Context, text string) (tts.AudioStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.texts = append(p.texts, text)
	p.mu.Unlock()
	return emptyAudioStream{}, nil
}

func (p *capturingTTSProvider) LastText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.texts) == 0 {
		return ""
	}
	return p.texts[len(p.texts)-1]
}

type emptyAudioStream struct{}

func (emptyAudioStream) Read() ([]byte, error) { return nil, io.EOF }
func (emptyAudioStream) Close() error          { return nil }

func TestPCM16ByteAssemblerCarriesSplitSampleAcrossChunks(t *testing.T) {
	assembler := &pcm16ByteAssembler{}

	first := assembler.Push([]byte{0x01, 0x02, 0x03})
	if got, want := first, []byte{0x01, 0x02}; !slices.Equal(got, want) {
		t.Fatalf("first chunk = %v, want %v", got, want)
	}
	if got := assembler.PendingLen(); got != 1 {
		t.Fatalf("pending len after first chunk = %d, want 1", got)
	}

	second := assembler.Push([]byte{0x04, 0x05, 0x06})
	if got, want := second, []byte{0x03, 0x04, 0x05, 0x06}; !slices.Equal(got, want) {
		t.Fatalf("second chunk = %v, want %v", got, want)
	}
	if got := assembler.PendingLen(); got != 0 {
		t.Fatalf("pending len after second chunk = %d, want 0", got)
	}

	samples := bytesToPCM16(append(first, second...))
	if got, want := samples, []int16{0x0201, 0x0403, 0x0605}; !slices.Equal(got, want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}
}

func TestPCM16ByteAssemblerHandlesSingleByteChunks(t *testing.T) {
	assembler := &pcm16ByteAssembler{}

	if got := assembler.Push([]byte{0x01}); len(got) != 0 {
		t.Fatalf("first single-byte chunk = %v, want empty", got)
	}
	if got := assembler.PendingLen(); got != 1 {
		t.Fatalf("pending len after first single-byte chunk = %d, want 1", got)
	}

	second := assembler.Push([]byte{0x02})
	if got, want := second, []byte{0x01, 0x02}; !slices.Equal(got, want) {
		t.Fatalf("second single-byte chunk = %v, want %v", got, want)
	}
	if got := assembler.PendingLen(); got != 0 {
		t.Fatalf("pending len after second single-byte chunk = %d, want 0", got)
	}
}

func TestPCM16ByteAssemblerKeepsEvenChunks(t *testing.T) {
	assembler := &pcm16ByteAssembler{}

	chunk := []byte{0x01, 0x02, 0x03, 0x04}
	if got := assembler.Push(chunk); !slices.Equal(got, chunk) {
		t.Fatalf("even chunk = %v, want %v", got, chunk)
	}
	if got := assembler.PendingLen(); got != 0 {
		t.Fatalf("pending len after even chunk = %d, want 0", got)
	}
}

func expectProviderCall(t *testing.T, calls <-chan string, want string) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("agent call text = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for agent call %q", want)
	}
}

func waitForCancelCount(t *testing.T, counter *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cancel count = %d, want %d", counter.Load(), want)
}

func TestSanitizeVoiceTextStripsExpressionTags(t *testing.T) {
	cases := map[string]string{
		"[happy] Yay! Let's play a game!": "Yay! Let's play a game!",
		"[zzzz] hi":                       "hi", // unknown lowercase tag still stripped (firmware parity)
		"[sleepy][silly] night":           "night",
		"[OK!] hi":                        "[OK!] hi", // uppercase/punctuation: not a tag
		"[3] hi":                          "[3] hi",   // digits: not a tag
		"[verylongtagnamex] hi":           "[verylongtagnamex] hi",
		"no tag here":                     "no tag here",
	}
	for in, want := range cases {
		if got := sanitizeVoiceTextForTTS(in); got != want {
			t.Errorf("sanitizeVoiceTextForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regression: the model re-tags mid-utterance, and an anchored strip left those
// tags in the TTS text, so the voice read "sleepy" aloud to the child.
func TestSanitizeVoiceTextStripsMidSentenceExpressionTags(t *testing.T) {
	cases := map[string]string{
		"[sleepy] aaah-hmmm... hello Rahul... [sleepy] the moon was asking about you... [sleepy] how was your day...": "aaah-hmmm... hello Rahul... the moon was asking about you... how was your day...",
		// Words either side of a dropped tag must not get glued together.
		"hello [silly] world":  "hello world",
		"one [happy][sad] two": "one two",
		// A tag at the very end leaves no trailing space.
		"goodnight [sleepy]": "goodnight",
		// Non-tags stay put wherever they appear.
		"pick [3] or [OK!] now": "pick [3] or [OK!] now",
	}
	for in, want := range cases {
		if got := sanitizeVoiceTextForTTS(in); got != want {
			t.Errorf("sanitizeVoiceTextForTTS(%q) = %q, want %q", in, got, want)
		}
	}

	// The face still keys off the leading tag only.
	if got := leadingExpressionTag("[sleepy] hi [happy] there"); got != "sleepy" {
		t.Errorf("leadingExpressionTag = %q, want sleepy", got)
	}
}

// Regression: the persona prompt ends a session with a "MEMO: ..." line and claims
// the gateway strips it. There is no gateway here, so the child heard it read aloud.
func TestSanitizeVoiceTextStripsMemoLine(t *testing.T) {
	cases := map[string]string{
		"MEMO: Aarav, six, beat his quiz streak.":        "",
		"  Memo: lowercase and indented count too.":      "",
		"[happy] MEMO: tagged memo still gets stripped.": "",
		"Bye for now!\nMEMO: Aarav loves dinosaurs.":     "Bye for now!",
		"That is a great memo: idea you had.":            "That is a great memo: idea you had.", // mid-line, not a memo line
		"I will remember that.":                          "I will remember that.",
	}
	for in, want := range cases {
		if got := sanitizeVoiceTextForTTS(in); got != want {
			t.Errorf("sanitizeVoiceTextForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}

// splitterSpeak feeds chunks rune-by-rune through a fresh sentenceSplitter the
// same way the pipeline's onChunk does, and returns every emitted sentence
// plus the flush remainder.
func splitterSpeak(chunks []string) []string {
	sp := newSentenceSplitter()
	var out []string
	for _, c := range chunks {
		for _, r := range c {
			if s := sp.Feed(r); s != "" {
				out = append(out, s)
			}
		}
	}
	if rem := sp.Flush(); rem != "" {
		out = append(out, rem)
	}
	return out
}

// Regression: the per-chunk MEMO strip only drops fragments that still START
// with "MEMO:" after sentence-splitting, so a multi-sentence memo body leaked
// to TTS from its second sentence on. The splitter now latches at the memo
// anchor and drops everything after it for the rest of the turn.
func TestSentenceSplitterMemoLatch(t *testing.T) {
	join := func(ss []string) string { return strings.Join(ss, " ") }

	// memo body with sentence punctuation, streamed across chunk boundaries
	spoken := join(splitterSpeak([]string{
		"[happy] Ting! Correct! ",
		"\nMEMO: type=daily_quiz | date=2026-08-01. ",
		"answered=3 | strengths=animals. parent_summary=Great day.",
	}))
	low := strings.ToLower(spoken)
	if strings.Contains(low, "memo") || strings.Contains(spoken, "answered=3") || strings.Contains(spoken, "parent_summary") {
		t.Fatalf("memo body leaked to TTS: %q", spoken)
	}
	if !strings.Contains(spoken, "Correct!") {
		t.Fatalf("legit speech before memo was lost: %q", spoken)
	}

	// memo on the same line as speech (model skipped the newline): sentence-
	// initial MEMO after "Correct!" must still latch
	spoken = join(splitterSpeak([]string{"Correct! MEMO: date=2026-08-01. answered=1."}))
	if strings.Contains(spoken, "answered=1") {
		t.Fatalf("same-line memo leaked: %q", spoken)
	}
	if !strings.Contains(spoken, "Correct!") {
		t.Fatalf("speech before same-line memo lost: %q", spoken)
	}

	// expression tag ahead of the memo still latches
	spoken = join(splitterSpeak([]string{"Bye for now!\n[neutral] MEMO: Aarav loves dinosaurs. He said so."}))
	if strings.Contains(spoken, "dinosaurs") {
		t.Fatalf("tagged memo leaked: %q", spoken)
	}
	if !strings.Contains(spoken, "Bye for now!") {
		t.Fatalf("speech before tagged memo lost: %q", spoken)
	}

	// once latched, later chunks stay dropped for the rest of the turn
	spoken = join(splitterSpeak([]string{"Done!\nMEMO: a=1. ", "still bookkeeping. ", "more bookkeeping."}))
	if strings.Contains(spoken, "bookkeeping") {
		t.Fatalf("post-latch chunk leaked: %q", spoken)
	}

	// mid-sentence "memo:" is normal speech and must survive in full
	spoken = join(splitterSpeak([]string{"I wrote a memo: buy milk. Fun!"}))
	if !strings.Contains(spoken, "buy milk") || !strings.Contains(spoken, "Fun!") {
		t.Fatalf("mid-sentence memo wrongly latched: %q", spoken)
	}

	// a fresh splitter (new turn) is unaffected by a previous turn's latch
	spoken = join(splitterSpeak([]string{"Hello again! How are you?"}))
	if !strings.Contains(spoken, "Hello again!") {
		t.Fatalf("fresh splitter broken: %q", spoken)
	}
}

// Regression (live session 3955eccc, 2026-08-01): gemma emitted a pseudo-tool
// call {"action":"write_file",...} as its entire reply and the JSON was read
// aloud to the child. A turn whose first meaningful content is '{' is never
// speech and must be dropped whole.
func TestSentenceSplitterJSONLatch(t *testing.T) {
	join := func(ss []string) string { return strings.Join(ss, " ") }

	// verbatim shape from the live session, streamed across chunks
	spoken := join(splitterSpeak([]string{
		`{ "action": "write_file", "action_input": { "content": "# Long-term`,
		` Memory. Stable facts. More sections here." } }`,
	}))
	if spoken != "" {
		t.Fatalf("pseudo-tool JSON leaked to TTS: %q", spoken)
	}

	// expression tag ahead of the JSON still latches
	spoken = join(splitterSpeak([]string{`[happy] {"action": "read_file", "path": "USER.md"}. Done.`}))
	if strings.Contains(spoken, "action") || strings.Contains(spoken, "Done.") {
		t.Fatalf("tagged JSON turn leaked: %q", spoken)
	}

	// braces mid-sentence after real speech began are normal and survive
	spoken = join(splitterSpeak([]string{"Math is fun {really}. Ask me more!"}))
	if !strings.Contains(spoken, "Math is fun") || !strings.Contains(spoken, "Ask me more!") {
		t.Fatalf("mid-turn braces wrongly latched: %q", spoken)
	}

	// normal speech is untouched
	spoken = join(splitterSpeak([]string{"[excited] Question seven! Which bird cannot fly?"}))
	if !strings.Contains(spoken, "Question seven!") {
		t.Fatalf("normal turn broken: %q", spoken)
	}
}

func TestSanitizeVoiceTextStripsReasoningJSON(t *testing.T) {
	cases := map[string]string{
		// Verbatim from a live session: gemma prefixed the reply with its thought.
		`{"thought": "The child said \"Tomorrow is my friend Ravi\". This is a fragmented sentence."} [excited] Ooh! [curious] Is it his birthday?`: "Ooh! Is it his birthday?",
		`{"reasoning":"picking a story"} Once upon a time.`:                                                                                       "Once upon a time.",
		`  {"analysis": "x"}   Hello there.`:                                                                                                      "Hello there.",
		`[neutral] {"thinking": "y"} Hello there.`:                                                                                                "Hello there.",
		// Not a leading reasoning object: must survive untouched.
		`I had a thought: dinosaurs are big.`: "I had a thought: dinosaurs are big.",
		`We can play a game.`:                 "We can play a game.",
	}
	for in, want := range cases {
		if got := sanitizeVoiceTextForTTS(in); got != want {
			t.Errorf("sanitizeVoiceTextForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeVoiceTextLowercasesShoutyCaps(t *testing.T) {
	cases := map[string]string{
		"BLAST OFF!":                   "blast off!", // Sarvam spells all-caps as letters
		"That is AMAZING, really HUGE": "That is amazing, really huge",
		"I watch TV in the USA":        "I watch TV in the USA", // allowlisted acronyms survive
		"plain text stays":             "plain text stays",
		"A":                            "A", // single letters untouched
	}
	for in, want := range cases {
		if got := sanitizeVoiceTextForTTS(in); got != want {
			t.Errorf("sanitizeVoiceTextForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSynthesizeDedupedAnnouncesFirstChunkWithTag(t *testing.T) {
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-a"},
		participant: &ParticipantState{identity: "device-a", sessionKey: "livekit:device:a"},
	}, nil, nil, nil)
	var announced []string
	pipeline.publishSpeechCreated = func(text string) { announced = append(announced, text) }

	deduper := &speechChunkDeduper{}
	pipeline.synthesizeDeduped(context.Background(), deduper, "[happy] Hello there, friend!")
	pipeline.synthesizeDeduped(context.Background(), deduper, "[sad] Second sentence goes here.")

	if len(announced) != 2 ||
		announced[0] != "[happy] Hello there, friend!" ||
		announced[1] != "[sad] Second sentence goes here." {
		t.Fatalf("expected each tagged chunk announced in order, got %v", announced)
	}
}

func TestLeadingExpressionTag(t *testing.T) {
	cases := map[string]string{
		"[happy] Yay!":        "happy",
		"  [sleepy] night":    "sleepy",
		"[sleepy][silly] hi":  "sleepy",
		"no tag":              "",
		"[OK!] not lowercase": "",
	}
	for in, want := range cases {
		if got := leadingExpressionTag(in); got != want {
			t.Errorf("leadingExpressionTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regression (dev session 2026-08-03): the memo latch buffered the finished
// question instead of emitting it, so the child heard nothing until the whole
// ~340-char MEMO line had been generated — a ~7s dead gap mid-turn. The pending
// sentence must reach TTS the moment the memo starts, not at Flush.
func TestSentenceSplitterMemoLatchEmitsPendingSpeechImmediately(t *testing.T) {
	sp := newSentenceSplitter()
	var duringFeed []string
	for _, r := range "[excited] Ting! Correct! [curious] Question three: which animal is the largest on land?\nMEMO: type=daily_quiz | answered=2 | awaiting=3" {
		if s := sp.Feed(r); s != "" {
			duringFeed = append(duringFeed, s)
		}
	}
	joined := strings.Join(duringFeed, " ")
	if !strings.Contains(joined, "largest on land") {
		t.Fatalf("question was not spoken until Flush; emitted during feed: %q", joined)
	}
	if strings.Contains(strings.ToLower(joined), "memo") || strings.Contains(joined, "awaiting=3") {
		t.Fatalf("memo body leaked: %q", joined)
	}
	if rem := sp.Flush(); strings.Contains(strings.ToLower(rem), "memo") {
		t.Fatalf("memo leaked via flush: %q", rem)
	}
}

// Partials are for barge-in, finals are for the LLM. With the realtime STT
// protocol streaming partials, dispatching on them started a turn per fragment
// and cancelled it on the next: three of five turns cancelled in a live session,
// and the child's answer split across them.
func TestRunInboundDoesNotDispatchOnPartials(t *testing.T) {
	results := make(chan stt.TranscriptEvent, 8)
	vadEvents := make(chan interface{}, 4)
	stream := &fakeTranscriptionStream{results: results}
	provider := &countingStreamingProvider{calls: make(chan string, 4)}
	bridge := &AgentBridge{
		provider:       provider,
		streamProvider: provider,
		asyncEventChan: make(chan AsyncEvent, 1),
	}
	pipeline := NewAudioPipeline(&RoomSession{
		roomInfo:    &livekitproto.Room{Name: "room-partial"},
		participant: &ParticipantState{identity: "device-p", sessionKey: "livekit:device:p"},
	}, bridge, nil, vadEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.RunInbound(ctx, stream)
		close(done)
	}()

	vadEvents <- vad.VADEvent{SpeechStart: true}
	// Partials building up, then a provider speech-end carrying no text — exactly
	// what Sarvam's vad.speech_end looks like.
	results <- stt.TranscriptEvent{Text: "National"}
	results <- stt.TranscriptEvent{Text: "National flag has"}
	results <- stt.TranscriptEvent{SpeechEnd: true}

	select {
	case got := <-provider.calls:
		t.Fatalf("dispatched on a partial: %q", got)
	case <-time.After(300 * time.Millisecond):
	}

	// The final is what may be sent.
	results <- stt.TranscriptEvent{Text: "National flag has three colors", IsFinal: true, SpeechEnd: true}

	select {
	case got := <-provider.calls:
		if got != "National flag has three colors" {
			t.Fatalf("agent call text = %q, want the final transcript", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dispatch on the final")
	}

	close(results)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}
