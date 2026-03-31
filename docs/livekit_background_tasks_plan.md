# LiveKit Voice Assistant Background Task Architecture Plan

## The Challenge
The current `AgentBridge` is strictly synchronous and request-response based:
`User Speaks -> VAD/Deepgram -> AgentBridge -> LLM -> TTS -> User Hears`

Background tasks (like `spawn`) return an `AsyncResult` immediately, but they complete later. The voice assistant currently has no way to "wake up" and speak when that background task finishes, because it only listens to the user's microphone.

## Proposed Architecture Plan for Low-Latency Voice Background Tasks

### Phase 1: Enable and Register Background Tools
Currently, the `spawn` and `subagent` tools are only registered in the heavy `AgentLoop`. We need to make them available to the lightweight `AgentBridge`.

1. **Update Configuration**: Enable `spawn`, `spawn_status`, and `subagent` tools in `~/.picoclaw/config.json`.
2. **Refactor Tool Registration**: Move the registration of shared tools (like `spawn`, `web_search`, etc.) out of `AgentLoop` and into a shared location so `AgentBridge` can load them during initialization in `cmd/picoclaw-livekit/main.go`.

### Phase 2: Asynchronous Event Pipeline
We need a way for background tasks to signal the voice assistant when they finish, without blocking the main audio pipeline.

1. **Create an Async Event Channel**: Add a new channel (e.g., `asyncEventChan`) to the `RoomSession` or `AudioPipeline`.
2. **Implement `AsyncCallback` in `AgentBridge`**: When `AgentBridge` executes a tool, it currently ignores the `AsyncCallback`. We will implement this callback so that when a background tool finishes, it sends its `ToolResult` to the `asyncEventChan`.

### Phase 3: Spontaneous Speech Generation (The "Wake Up" Mechanism)
When a background task finishes, the agent needs to proactively announce the result to the user, even if the user isn't speaking.

1. **Update `AudioPipeline.RunInbound`**: 
   Modify the main `select` loop in `audio_pipeline.go` to listen to the new `asyncEventChan` alongside VAD and Deepgram events.
2. **Handle Async Results**:
   When an event arrives on `asyncEventChan`:
   - Check if the user is currently speaking (using the `vadSpeechEnded` flag).
   - **If the user IS speaking**: Silently append the background task result to the conversation history (`SessionStore`). The LLM will naturally see it the next time it generates a response.
   - **If the user IS NOT speaking**: 
     1. Append the result to the conversation history as a "system" or "tool" message.
     2. Trigger a *spontaneous* LLM generation (e.g., "The background task just finished, here is the result...").
     3. Stream the response directly to TTS.

### Phase 4: Interruption Handling
Because the agent might start speaking spontaneously, we must ensure the user can still interrupt it.

1. **Leverage Existing VAD**: The VAD integration we just built is perfect for this. If the agent starts announcing a background task result and the user says "Stop", the VAD `SpeechStart` event will immediately cancel the TTS, just like it does for normal responses.

## Why this maintains low latency:
- The main audio pipeline (`RunInbound`) remains a fast, non-blocking `select` loop.
- Background tasks run in completely separate goroutines (subagents).
- The initial acknowledgment ("I'll start that in the background") happens instantly because the `spawn` tool returns immediately.
- The spontaneous announcement only triggers when the system is idle, preventing audio collisions.
