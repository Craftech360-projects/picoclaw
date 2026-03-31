# Background Task Feature for Voice Agent

This implements the architecture plan for low-latency background tasks for the LiveKit voice agent, allowing background tools like `spawn` to run and spontaneously signal the voice agent to announce their results.

## Proposed Changes

### 1. Refactor Tool Registration in Core Agent
Moved `registerSharedTools` from `pkg/agent/loop.go` to a public function so `AgentBridge` can access shared tools without instantiating the heavy `AgentLoop`.

#### [MODIFY] [loop.go](file:///D:/picoclaw/pkg/agent/loop.go)
- Extract the `registerSharedTools` block into a new public function `RegisterSharedTools` (or a helper in `tools`).
- Modify the `AgentLoop`'s usage to call this new public function.

#### [NEW] [shared_tools.go](file:///D:/picoclaw/pkg/agent/shared_tools.go)
- Define `RegisterSharedTools` which adds all shared tools (`spawn`, `subagent`, `web`, `i2c`, `message`, etc.) to a given `ToolRegistry`.
- Expose the required spawner logic so different environments (like `AgentBridge`) can inject their own `SubTurnSpawner` implementation, preventing the shared registration from depending tightly on `AgentLoop`.

---

### 2. Configure AgentBridge and Worker
Add tool registration to `picoclaw-livekit`, wire up the asynchronous event channel, and implement a custom spawner for the `spawn` tool that works natively within the `AgentBridge` context.

#### [MODIFY] [main.go](file:///D:/picoclaw/cmd/picoclaw-livekit/main.go)
- Initialize shared tools on the `agentInstance` before returning it, using the newly exposed `agent.RegisterSharedTools`.
- Implement a simple `SubTurnSpawner` for `main.go` that can handle the synchronous execution of `spawn` tools by recursively invoking a temporary `AgentBridge` or passing it to `AgentInstance`.

#### [MODIFY] [agent_bridge.go](file:///D:/picoclaw/pkg/livekit/agent_bridge.go)
- Add `AsyncEventChan chan *tools.ToolResult` to `AgentBridgeConfig` and `AgentBridge`.
- Implement testing for `tc.AsyncCallback`. When the `executeTool` method is invoked, pass an anonymous function as the `asyncCb` that sends the finished `ToolResult` back to `AsyncEventChan` allowing asynchronous tracking of the background tool.

---

### 3. Spontaneous Speech Generation (Wake Up Mechanism)
Wire the LiveKit room session components to react to background task completions dynamically by interacting with the `asyncEventChan`.

#### [MODIFY] [audio_pipeline.go](file:///D:/picoclaw/pkg/livekit/audio_pipeline.go)
- Modify the main `select` loop inside `RunInbound` to listen to `ab.AsyncEventChan` alongside Deepgram and VAD events.
- When an event is received from the channel:
  - Check if the user is actively speaking via `!ap.session.participant.speaking.Load()` (or tracking `vadSpeechEnded`).
  - If **currently speaking**, append the background task result stealthily to the conversation history as a system message.
  - If **not speaking**, append the result to conversation history as a system message, and invoke a spontaneous LLM generation via `ap.HandleUtterance` to "wake up" the agent and announce the new result over TTS.

#### [MODIFY] [room_session.go](file:///D:/picoclaw/pkg/livekit/room_session.go)
- Wire `asyncEventChan` created per session into `AgentBridgeConfig` and `AudioPipeline` so it correctly maps async events back to the specific user/room.

## User Review Required

- Is a basic spontaneous `system` message like "The background task just finished, here is the result..." acceptable for the "Wake up" trigger? 
- For the `cmd/picoclaw-livekit/main.go` SubTurnSpawner, should we use a completely new `AgentBridge` instance per subagent, or re-use existing infrastructure from `AgentInstance`? (A new instance prevents shared state collisions).

## Verification Plan

### Automated/Manual Verification
1. I will verify that `picoclaw-livekit` compiles successfully.
2. I will ensure all unit tests pass, tracking any breakages in `AgentLoop` tests.
3. Reviewer will need to test via client app that triggering a slow background tool successfully speaks an announcement when finished without blocking intermediate conversation.
