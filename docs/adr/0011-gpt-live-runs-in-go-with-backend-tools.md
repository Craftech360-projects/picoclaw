# 11. GPT-Live Runs in Go; Character Logic Moves to Backend Tools

Date: 2026-09-12

## Status

Proposed

Builds on [ADR-0002](0002-manager-owns-character-to-runtime-agent-routing.md) (the manager
routes a character to a runtime agent name) and [ADR-0005](0005-quizzy-scored-questions-come-from-a-curated-bank.md)
(scored questions come from the bank). Neither changes. What changes is *who* produces
the quiz verdict: a function tool called by a backend model, not a `MEMO:` line the
voice model writes into its reply.

## Context

OpenAI's GPT-Live is a full-duplex voice model: it listens while it speaks, decides when
each turn starts and ends, and hands reasoning and tool calls to a backend Responses
model so a slow lookup does not leave the child in dead air. A Python worker
(`cheeko-backend/main/python-agent`) proved it end to end on the dev box on 2026-09-11:
greeting, Indian-accent persona, filler while delegating, backend tool call, correct
answer spoken, 16 kHz audio both ways.

The product team wants the production voice agent to stay in Go, in this repo, and to
keep the `AGENT.md` / `SOUL.md` / `USER.md` persona that the manager regenerates every
session. No Go framework speaks the GPT-Live protocol: LiveKit's Agents framework is
Python and Node only, the LiveKit Go server SDK is a room/media SDK, and OpenAI's Go SDK
`live` package covers the WebRTC/SIP control plane only (session config types, no
websocket event loop, no server event types).

Three properties of GPT-Live collide with how `pkg/livekit` works today:

1. **The voice model owns turns and barge-in.** Push-to-talk, TEN VAD turn detection,
   `response.cancel`-style interruption, sentence splitting and TTS dedupe in
   `audio_pipeline.go` have no role. The framework can only stop *playing* audio; the
   model keeps talking until it stops on its own.
2. **Persona and voice are immutable after `session.start`.** The per-turn system
   messages the bridge appends today (voice directive, language lock, quiz Door
   directive at `agent_bridge.go:1384`) cannot be re-sent. Mid-session guidance goes
   through three append channels capped at 500 tokens each: instructions (standing
   rule), thinking (silent context), commentary (say this now, in your own words).
3. **The voice model never sees tools and cannot emit structured text reliably.** Its
   transcript arrives *after* the audio. The `MEMO: type=daily_quiz | ...` line that
   `quiz_state.go:220` parses from the assistant reply will not exist. Scoring has to
   move to where the reasoning model is: a function tool on the backend.

Today Quizzy, Bujho, Ginti, Cheeko and the content characters run with **zero tools**
(`cmd/picoclaw-livekit/workspace_tools.go:384`). Their persona says "never call tools",
questions arrive through prompt placeholders (`{{QUIZ_QUESTIONS}}`), the day's state is
re-injected from `memory/state/*.md` every turn, and verdicts are parsed from text.
Under GPT-Live every one of those channels changes shape.

## Decision

### 1. A standalone `pkg/gptlive` package, no LiveKit imports

The package owns the wire protocol and the duplex semantics, and nothing else:

- Own wire types mirroring the LiveKit plugin's `gpt_live_types.py` (about 40 events).
  We do not bump `openai-go` from 3.22 to 3.61 for this; its `live` package does not
  carry the websocket events we need and the bump would touch every provider.
- `Dial` → `session.start` → wait for `session.started` before any other event, exactly
  as the protocol requires. `session.close` on shutdown, wait up to 5 s for
  `session.closed` because it carries the final usage.
- Audio in as PCM16 at the session's rate, audio out as a channel of PCM16 frames.
  **Rate is a per-session setting**: `16000` for device rooms (the gateway path already
  runs at 16 kHz, so no resampling anywhere), `24000` for browser/dashboard rooms.
  Verified 2026-09-12: OpenAI accepts `rate: 16000` and returns 16 kHz audio.
- Delegation to a backend Responses model with function tools. `client` delegation is
  out of scope for the first release.
- Reconnect with startup-history replay (max 128 items), backoff 2 s, 3 retries, and an
  optional max session duration, matching the Python plugin.
- An adaptive noise gate that segments the continuous output stream into turns
  (activation 3.0×, deactivation 1.8× of the learned floor, 0.5 s min silence, 10 s
  window, floor ≥ 1e-4, idle timeout 0.8 s). Turn boundaries drive the agent state
  attribute and transcript segment ids the dashboard and gateway already read.

### 2. A second pipeline in `pkg/livekit`, selected per worker process

`gptlive_pipeline.go` bridges a `RoomSession` to a `gptlive.Session`. It replaces
`AudioPipeline` + the bridge's LLM loop for that session and reuses everything else:
`worker.go`, `room_session.go` (join, tracks, data channel), workspace hydration,
`quiz_bank.go` fetch and reporters, `post_session_persistence.go`.

The pipeline is chosen by the worker process, not per room:
`PICOCLAW_LIVEKIT_PIPELINE=gptlive`. The dev box runs it as a second pm2 app registered
under a different LiveKit agent name (`cheeko-gptlive`), and the manager's
`runtime_agent_name` routes characters to it one at a time (ADR-0002). The cascade
worker is untouched.

Per-session voice, accent and sample rate ride in the dispatch metadata `gptlive`
block the admin dashboard already sends: `{"voice":"marin","accent":"indian","rate":16000}`.

### 3. No push-to-talk, no local VAD, no interruption plumbing

Mic audio streams continuously. The `ptt_event`, `speech_end` and `abort` data messages
are ignored by this pipeline. Interruption is the model's; the pipeline only stops
writing to the local track when a burst closes. A device-side playback cut is a later,
measured addition, not part of this decision. Echo cancellation must run on the device;
the browser has it built in.

### 4. Persona: the same files, composed once, at session start

Voice instructions = `agent.ContextBuilder.BuildSystemPrompt()` (identity, `AGENT.md`,
`SOUL.md`, `USER.md`, skills summary, joined by `---`) + a delegation block + an
optional accent block. That is the same text the cascade sends as its system prompt,
so the manager-owned persona keeps working unchanged. Language lock and voice
directive fold into the delegation block, since nothing can be appended per turn.

Backend instructions = a short operator note (child-safe, one or two sentences, use
tools for facts) + the character's rendered bank block (`RenderQuizQuestions` /
`RenderContentBank` output) so the backend can score without a second fetch.

Greeting = `AppendCommentary(buildGreetingInstruction(character, greetingPrompt))`,
sent when the device's `ready_for_greeting` arrives or after 3 s, whichever first.
The model may decline a commentary; the pipeline logs it and moves on.

### 5. Character logic becomes backend function tools

The tool registry stays the source of truth (`pkg/tools.ToolRegistry`, JSON-schema
`Parameters()`). Its `ToProviderDefs()` output (`{"type":"function","function":{...}}`)
is flattened to the Responses shape `{"type":"function","name","description","parameters"}`
the backend expects. Hosted web search is offered as `{"type":"web_search"}` when the
character allows it.

Per-character tool sets replace `liveKitToollessCharacters`:

| Character(s) | Tools offered to the backend |
|---|---|
| Cheeko, Chanda, Masti, Tara, Nani, Mitthu, Tikku | `get_time_date`, `get_weather`, `web_search` (hosted), `read_file` (skills), `remember_child_fact` |
| Quizzy (`daily_quiz`), Bujho (`daily_riddle`), Ginti (`daily_math`) | the above plus `quiz_status`, `quiz_score_answer`, `quiz_record_wonder` |
| Story / jokes / why / words / spell characters | the above plus `content_next` |

`write_file` is not offered; `remember_child_fact` writes the two files the write guard
allowed (`user.md`, `memory/memory.md`) with a fixed shape. `exec`, `list_dir`,
`web_fetch` are not offered in the first release.

**Quiz under GPT-Live.** The day's numbered questions (with answers) are in *both* the
voice instructions and the backend instructions. The voice model asks them itself, no
round trip. When the child answers, the persona tells the voice model to delegate; the
backend reads the transcript against the bank and calls
`quiz_score_answer(question_id, result, transcript)`. The tool:

- validates `question_id` against the batch and `result ∈ {correct, wrong, revealed}`,
  exactly as `parseQuizVerdict` does today;
- tracks tries per question and applies the Door ladder (`DoorFor(tries)`) and the
  mastery rule (a `correct` at Door 3 is recorded as `revealed`);
- writes `memory/state/<type>.md` with the same `MEMO: type=... | date=... | scored_q=...
  | result=... | answered=...` line the cascade writes, so `RestoreCharacterState`,
  `PruneStaleStateFiles` and the manager's `progress/state` keep working untouched;
- calls the existing `NewQuizAnswerReporter` / `NewQuizAttemptReporter` closures;
- returns the next Door directive as its result text, and the pipeline also sends it
  with `AppendInstructions` so the voice model follows it on the next turn.

The wonder question uses `quiz_record_wonder(question, answer, code)` → the existing
`NewWonderQuestionReporter`. `quiz_status` returns `STATUS:` (answered, pending id,
door) so the backend can answer "which question are we on" without guessing.

### 6. Persistence stays where it is

Transcripts accumulate in the session history (user turns from input transcript deltas,
agent turns from closed bursts). At teardown the pipeline hands that list to the same
chat-history, summary and token-usage uploads `post_session_persistence.go` performs
for the cascade. Voice seconds come from `session.usage.updated`; backend tokens from
`response.completed`.

## Consequences

- One protocol client to maintain against an alpha API that LiveKit tracks for free in
  Python. Budget for upkeep when OpenAI changes events.
- Scripted lines are gone for GPT-Live characters: announcements become commentary the
  model rephrases. Anything that needs word-for-word speech stays on the cascade.
- Quiz correctness now depends on the backend model reading the transcript, with the
  tool enforcing the bank, doors and mastery. The attempt log and the MEMO files are
  unchanged on disk and on the server, so parents' progress views need no change.
- The `agent_state_changed` data packet, the `lk.agent.state` attribute and
  `lk.transcription` text streams are emitted by the new pipeline in the same shapes,
  so the dashboard, gateway and device display work without changes.
- Device firmware must stream the mic continuously with echo cancellation. Push-to-talk
  becomes a mute toggle at most.
