# LiveKit Per-Session Turn Controller Design

## Context

The LiveKit voice path is a pipeline: VAD and STT produce a completed utterance, `AudioPipeline.RunInbound` starts `HandleUtterance` in a goroutine, `AgentBridge.ChatStream` calls the LLM, and TTS speaks streamed chunks. Tool calls run asynchronously and then trigger another LLM iteration when tool results are ready.

The current implementation cancels the most recent TTS context on barge-in, but it does not model a session-level owner for the active conversational turn. When STT emits multiple final utterances close together after a barge-in, each flush can start a new `HandleUtterance` goroutine. Older LLM/tool continuations can still call their callbacks and speak if they were not the latest registered TTS context at the moment of cancellation.

## Goal

For each LiveKit session, only the current user turn may produce assistant speech or finish-state callbacks. A new real user utterance must cancel and supersede any previous in-flight user, tool-continuation, or spontaneous assistant turn for that same session.

## Non-Goals

- Do not redesign VAD/STT buffering.
- Do not change provider APIs.
- Do not add a new queueing system.
- Do not solve sports-source extraction quality in this change; that belongs to a separate response-grounding pass.

## Proposed Design

Add a small per-session turn controller owned by `AudioPipeline`.

The controller tracks:

- monotonically increasing `turnID`
- current `context.CancelFunc`
- cancellation reason

When `RunInbound` flushes a real user utterance:

1. Cancel the previous active turn for that pipeline/session.
2. Create a new turn context derived from the inbound loop context.
3. Register the new turn as active.
4. Start `HandleUtterance` with that turn context.
5. Wrap chunk and done callbacks so they only run if their turn is still active.
6. Clear the active turn only when the same turn completes.

When transcript-confirmed barge-in happens:

1. Cancel active TTS audio as today.
2. Cancel the active turn context as `stt_transcript_after_vad`.
3. Wait for final STT text to start the next turn.

When spontaneous background results or greeting speech starts:

- Use the same controller path so spontaneous speech cannot overlap with later user utterances.
- If the user starts speaking, the spontaneous turn is canceled.

## Expected Behavior

- If the user interrupts an answer and then STT emits "No..." followed by "Just need to tell", only the latest active turn is allowed to speak.
- Tool continuations from a canceled turn may finish tool execution, but their follow-up LLM/TTS callbacks must not speak.
- A canceled turn must not call the final `onDone` path that flips the visible agent state back incorrectly after a newer turn has started.
- Same-text duplicate suppression remains as-is.
- VAD-only noise still must not cancel assistant speech until transcript text confirms user speech.

## Files

- `pkg/livekit/audio_pipeline.go`
  - Add turn controller type and methods.
  - Use it in `RunInbound.flushBufferedUtterance`, transcript-confirmed barge-in, `HandleUtterance` callbacks, greeting, and spontaneous responses.
- `pkg/livekit/audio_pipeline_test.go`
  - Add tests for superseding user turns and callback suppression.
  - Keep existing duplicate and barge-in tests passing.

## Testing Strategy

- Unit test: a new utterance cancels the previous in-flight provider call.
- Unit test: stale chunks from the previous turn are suppressed after a newer turn starts.
- Unit test: VAD-only start still does not cancel TTS.
- Regression tests: existing LiveKit package tests continue to pass.

## Risks

- Canceling tool execution too aggressively could drop useful results. The design permits tools to finish if they already started, but blocks canceled turns from speaking stale follow-up text.
- Over-wrapping callbacks could accidentally suppress valid speech. Tests should verify the current turn speaks normally and stale turns do not.
- Agent state transitions can race. The design requires `onDone` to be gated by turn ownership.

## Approval

Approved approach: per-session turn controller.
