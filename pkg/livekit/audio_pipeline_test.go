package livekit

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
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
	pipeline.publishSpeechCreated = func() {
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
	pipeline.publishSpeechCreated = func() {
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
	pipeline.publishSpeechCreated = func() {
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
	pipeline.publishSpeechCreated = func() {
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
