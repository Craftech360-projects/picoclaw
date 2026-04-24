# LiveKit Per-Session Turn Controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure each LiveKit session has exactly one active conversational turn, and newer user speech cancels/supersedes older LLM/TTS/tool-continuation callbacks.

**Architecture:** Add a small turn controller inside `AudioPipeline` that owns a monotonically increasing turn ID and cancel function. Every user utterance, greeting, and spontaneous response runs through a guarded turn context; callbacks check ownership before speaking or marking done.

**Tech Stack:** Go, LiveKit voice pipeline, existing STT/VAD/TTS interfaces, existing Go test suite.

---

## File Map

- Modify `pkg/livekit/audio_pipeline.go`: add `voiceTurnController`, wire it into user utterances, transcript-confirmed barge-in, greeting, and spontaneous responses.
- Modify `pkg/livekit/audio_pipeline_test.go`: add TDD regression tests for superseding active turns and stale callback suppression.

---

### Task 1: Add a failing test for canceling the previous turn

**Files:**
- Modify: `pkg/livekit/audio_pipeline_test.go`

- [ ] **Step 1: Add a blocking provider test double**

Add this helper near the existing provider test doubles:

```go
type blockingStreamingProvider struct {
	started chan string
	release chan struct{}
}

func newBlockingStreamingProvider() *blockingStreamingProvider {
	return &blockingStreamingProvider{
		started: make(chan string, 4),
		release: make(chan struct{}),
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
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			p.started <- messages[i].Content
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		if onChunk != nil {
			onChunk("finished")
		}
		return &providers.LLMResponse{Content: "finished"}, nil
	}
}

func (p *blockingStreamingProvider) GetDefaultModel() string { return "test" }
```

- [ ] **Step 2: Add the regression test**

Add this test:

```go
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

	vadEvents <- vad.VADEvent{SpeechStart: true}
	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "first question", IsFinal: true}
	expectProviderCall(t, provider.started, "first question")

	vadEvents <- vad.VADEvent{SpeechStart: true}
	results <- stt.TranscriptEvent{Text: "second question", IsFinal: false}
	vadEvents <- vad.VADEvent{SpeechEnd: true}
	results <- stt.TranscriptEvent{Text: "second question", IsFinal: true}
	expectProviderCall(t, provider.started, "second question")

	close(provider.release)
	close(results)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunInbound did not exit after results channel closed")
	}
}
```

- [ ] **Step 3: Run the failing test**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go test ./pkg/livekit -run TestRunInboundNewUtteranceCancelsPreviousTurn -count=1
```

Expected before implementation: failure or hang because the second turn does not reliably cancel/supersede the first active turn.

---

### Task 2: Implement the turn controller

**Files:**
- Modify: `pkg/livekit/audio_pipeline.go`

- [ ] **Step 1: Add controller fields and methods**

Add `sync` to imports and add these types near `AudioPipeline`:

```go
type voiceTurnController struct {
	mu     sync.Mutex
	nextID uint64
	active voiceTurn
}

type voiceTurn struct {
	id     uint64
	ctx    context.Context
	cancel context.CancelFunc
	reason string
}

func (c *voiceTurnController) Start(parent context.Context, reason string) voiceTurn {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel != nil {
		c.active.reason = reason
		c.active.cancel()
	}
	c.nextID++
	ctx, cancel := context.WithCancel(parent)
	c.active = voiceTurn{id: c.nextID, ctx: ctx, cancel: cancel}
	return c.active
}

func (c *voiceTurnController) Cancel(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel == nil {
		return
	}
	c.active.reason = reason
	c.active.cancel()
}

func (c *voiceTurnController) IsActive(turn voiceTurn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return turn.id != 0 && c.active.id == turn.id && c.active.cancel != nil
}

func (c *voiceTurnController) Finish(turn voiceTurn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn.id != 0 && c.active.id == turn.id {
		c.active = voiceTurn{}
	}
}
```

Add this field to `AudioPipeline`:

```go
turns voiceTurnController
```

- [ ] **Step 2: Use the controller for user utterances**

In `flushBufferedUtterance`, replace the raw background context with:

```go
turn := ap.turns.Start(ctx, "new_user_utterance")
ap.setTTSCancel(turn.cancel)

go func() {
	defer ap.turns.Finish(turn)
	_, _ = ap.HandleUtterance(turn.ctx, sessionKey, text, turn.cancel)
}()
```

- [ ] **Step 3: Cancel the active turn on confirmed barge-in**

After `ap.cancelTTS("stt_transcript_after_vad")`, add:

```go
ap.turns.Cancel("stt_transcript_after_vad")
```

- [ ] **Step 4: Run the test**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go test ./pkg/livekit -run TestRunInboundNewUtteranceCancelsPreviousTurn -count=1
```

Expected: PASS.

---

### Task 3: Guard callbacks against stale turns

**Files:**
- Modify: `pkg/livekit/audio_pipeline.go`
- Modify: `pkg/livekit/audio_pipeline_test.go`

- [ ] **Step 1: Add `HandleUtteranceForTurn` wrapper**

Add a method next to `HandleUtterance`:

```go
func (ap *AudioPipeline) HandleUtteranceForTurn(turn voiceTurn, sessionKey string, text string) (bool, error) {
	return ap.HandleUtterance(turn.ctx, sessionKey, text, func() {
		if !ap.turns.IsActive(turn) {
			return
		}
		turn.cancel()
	})
}
```

Then use it in `flushBufferedUtterance`:

```go
go func() {
	defer ap.turns.Finish(turn)
	_, _ = ap.HandleUtteranceForTurn(turn, sessionKey, text)
}()
```

- [ ] **Step 2: Gate `HandleUtterance` callbacks**

Inside `HandleUtterance`, before speaking chunks in `onChunk`, add:

```go
select {
case <-ctx.Done():
	return
default:
}
```

At the top of `onDoneCallback`, add:

```go
select {
case <-ctx.Done():
	return
default:
}
```

- [ ] **Step 3: Add a stale callback test**

Add a provider that sends one chunk after release and verify only the current turn reaches the provider/TTS-facing path. Use the existing `blockingStreamingProvider`; after starting two turns and releasing, assert no extra provider call appears beyond the two expected starts:

```go
select {
case got := <-provider.started:
	t.Fatalf("unexpected extra provider call after superseded turn: %q", got)
case <-time.After(150 * time.Millisecond):
}
```

- [ ] **Step 4: Run focused tests**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go test ./pkg/livekit -run 'TestRunInbound(NewUtteranceCancelsPreviousTurn|SuppressesDuplicateSTTSpeechEndAfterVADFlush|DoesNotCancelTTSOnVADStartWithoutTranscript|CancelsTTSWhenTranscriptArrivesAfterVADStart)' -count=1
```

Expected: PASS.

---

### Task 4: Apply controller to greeting and spontaneous responses

**Files:**
- Modify: `pkg/livekit/audio_pipeline.go`

- [ ] **Step 1: Wrap greeting**

In `TriggerGreeting`, start a turn:

```go
turn := ap.turns.Start(ctx, "greeting")
ap.setTTSCancel(turn.cancel)
```

Use `turn.ctx` for `GenerateGreeting`, `synthesizeDeduped`, and `flushSilence`. In the done callback, return immediately if `!ap.turns.IsActive(turn)`, and call `ap.turns.Finish(turn)` at the end of the goroutine.

- [ ] **Step 2: Wrap spontaneous responses**

In `handleAsyncEvent`, replace `context.Background()` with:

```go
turn := ap.turns.Start(context.Background(), "background_task_result")
ap.setTTSCancel(turn.cancel)
```

Use `turn.ctx`, guard callbacks with `ap.turns.IsActive(turn)`, and finish only the matching turn.

- [ ] **Step 3: Run LiveKit tests**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go test ./pkg/livekit -count=1
```

Expected: PASS.

---

### Task 5: Final verification

**Files:**
- No source changes unless verification reveals a failure.

- [ ] **Step 1: Format**

Run:

```powershell
gofmt -w pkg\livekit\audio_pipeline.go pkg\livekit\audio_pipeline_test.go
```

- [ ] **Step 2: Run package tests**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go test ./pkg/livekit ./cmd/picoclaw-livekit -count=1
```

Expected: PASS.

- [ ] **Step 3: Build worker**

Run:

```powershell
$env:PATH = "D:\picoclaw;C:\msys64\mingw64\bin;$env:PATH"
go build -o $env:TEMP\picoclaw-livekit-turn-controller-check.exe ./cmd/picoclaw-livekit
```

Expected: exit code 0.

- [ ] **Step 4: Review diff**

Run:

```powershell
git diff -- pkg\livekit\audio_pipeline.go pkg\livekit\audio_pipeline_test.go
git diff --check
```

Expected: only scoped LiveKit turn-controller changes; no whitespace errors.

