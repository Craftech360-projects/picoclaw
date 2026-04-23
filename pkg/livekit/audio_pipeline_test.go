package livekit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/sipeed/picoclaw/pkg/providers"
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
