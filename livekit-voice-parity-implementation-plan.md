# LiveKit Voice Agent Parity & Hardening Plan (P0 + P1 + P2)

## Summary
Implement full parity-focused roadmap for LiveKit voice runtime relative to default gateway, prioritized to preserve voice UX first (latency, interruption, reliability), then add operability (events/config), then optimization.
Delivery is split into three milestones so each stage is shippable and measurable.

## Key Changes

### 1. P0 - Core Voice Reliability & Functional Parity
- **Task 1: Interruption/barge-in hardening**
  - Normalize turn-cancel semantics across greeting, user turns, async announcements, and cron-triggered speech.
  - Ensure immediate TTS stop + queue flush + state transition consistency (`thinking/speaking/listening`) under overlap races.
- **Task 2: Cron runtime parity completion**
  - Finalize scheduled-job lifecycle for LiveKit: create, persist, execute, announce, and fail visibly.
  - Ensure command and non-command cron jobs follow explicit policy in voice runtime.
- **Task 3: Rate-limit resilience for proactive/system turns**
  - Extend cooldown/fallback strategy to all proactive paths (greeting + spontaneous background announcements).
  - Prevent retry storms and guarantee child-friendly fallback speech when provider is unavailable.
- **Task 4: Latency budget instrumentation**
  - Add per-turn timing markers: STT first partial/final, LLM first token/final token, TTS first audio/final audio, total E2E.
  - Emit structured logs/counters for percentile tracking and regression detection.

### 2. P1 - Operability & Runtime Controls
- **Task 5: AgentBridge event surface**
  - Add lightweight event bus for LiveKit runtime parity with gateway observability (turn start/end, tool call start/end, retries, errors, fallback used, interruption cause).
- **Task 6: Runtime policy/config controls**
  - Promote key voice knobs to explicit config (VAD thresholds, greeting mode, fallback/cooldown durations, async announce mode).
  - Validate config at startup with safe defaults and explicit warnings.
- **Task 7: Async announcement policy engine**
  - Define and implement deterministic policy: immediate speak vs queue vs silent history append based on speaking state, event type, and priority.

### 3. P2 - Optimization & Long-Term Robustness
- **Task 8: Provider strategy profiles**
  - Introduce provider/profile selection for short-form voice turns (greeting/system) vs normal conversation turns to reduce cost/latency/rate-limit exposure.
- **Task 9: Session quality scoring**
  - Compute per-session quality summary (fallback count, interruption recovery success, median TTFT, error count).
- **Task 10: Replay/debug trace bundle**
  - Add exportable trace artifact for problematic sessions (timeline + decisions + errors + provider responses metadata, excluding sensitive content as configured).

## Public Interfaces / API / Types to Add or Update
- Add a **LiveKit runtime config block** for voice-specific controls (cooldowns, announce policy, VAD and fallback knobs).
- Add a **structured LiveKit event schema** (event kind, timestamps, session key, turn id, cause/error metadata).
- Add a **session quality report shape** for logs/metrics export.
- Extend cron-runtime contract in LiveKit so scheduled execution outcomes are explicitly typed (success/failure/announced/skipped).

## Test Plan
1. **Interruption/Concurrency**
   - User barges in during greeting, during tool-iteration speech, and during cron announcement; verify no stuck speaking state and no leaked TTS playback.
2. **Cron End-to-End**
   - Create one-time and recurring cron jobs; verify execution path, response generation, announcement behavior, and persisted state transitions.
3. **Rate-Limit/Outage**
   - Simulate provider 429 and transient failures for greeting and async announcements; verify cooldown activation, fallback speech, and recovery.
4. **Latency Instrumentation**
   - Verify timing fields exist and are monotonic; assert metrics emitted for success, cancel, and failure paths.
5. **Config Validation**
   - Invalid runtime knob values fail fast or clamp to safe defaults with warnings.
6. **Event Surface**
   - Assert all expected event kinds fire once per lifecycle stage and include required metadata.
7. **Regression Safety**
   - Existing LiveKit audio pipeline tests, cron tests, and compilation checks remain green.

## Assumptions and Defaults
- Scope includes **all P0 + P1 + P2** as requested.
- Voice UX takes precedence over strict gateway architectural parity where they conflict.
- No full multi-channel gateway stack (telegram/slack/etc.) will be embedded into LiveKit runtime.
- Rollout order: **P0 first**, then P1, then P2; each milestone requires passing regression + scenario tests before next milestone starts.
