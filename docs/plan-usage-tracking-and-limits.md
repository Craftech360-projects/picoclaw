# Plan — Usage-Tracking Fixes & Per-Device Limits

> Created 2026-07-11 from a code audit of the tracking path (picoclaw worker → manager-api → Postgres),
> in preparation for the subscription system. Costing reference: `docs/cheeko-costing-sheet.xlsx`.
> Audit verdict: token counting is correct (billed = recorded); 4 holes below, ranked by risk.

## Context — how tracking works today

- **Worker (this repo):** every LLM turn calls `recordUsage()` with provider-reported
  `PromptTokens`/`CompletionTokens` (`pkg/livekit/agent_bridge.go:1759`, call site `:666`).
  Greeting + tool iterations share this path via `runIterationWithProfile`.
  At session end, `post_session_persistence.go` POSTs one summary to
  `manager-api /device/token-usage` (tokens, duration = bridge lifetime, message count).
- **Manager API:** `recordTokenUsage` (`src/services/device.service.js:877`) upserts
  `voice_sessions` + `device_token_usage_session` (idempotent by `session_id`) + daily aggregate.
- **`device_usage_daily.ai_talk_usage_seconds` is DEVICE-reported** (`ai_talk_start/end` analytics
  events) and demonstrably incomplete — do NOT use it for limits. Use `device_token_usage_session`.

---

## Phase 1 — Fix tracking holes (this repo + manager-api)

### 1.1 Request usage in streaming calls  ⚠ highest risk
**Problem:** `pkg/providers/openai_compat/provider.go` parses `usage` from SSE chunks but never sends
`stream_options: {"include_usage": true}`. Works today only because OpenRouter volunteers usage.
Switching to OpenAI-direct (planned option for gpt-4.1-mini) → `usage == nil` → 0 tokens recorded
silently while the real bill continues.

- [ ] Add `stream_options: {"include_usage": true}` to the streaming request body (openai_compat).
- [ ] In `recordUsage` (`agent_bridge.go:1759`): when `usage == nil`, log a WARN with model/provider
      so silent undercount becomes visible.
- **Verify:** unit test — streamed response with/without usage chunk; integration: one turn via
  OpenRouter AND one via OpenAI-direct, both produce non-zero `input_tokens` in the POST body.

### 1.2 Always persist session duration
**Problem:** persistence is skipped when no tokens & no transcript
(`post_session_persistence.go:38`), and the usage POST is skipped when input+output tokens are 0
(`:192`). A child who connects and never speaks leaves NO duration row → minutes/day limits
undercount.

- [ ] Remove/relax both guards: always send the usage summary when `SessionDurationSeconds > 0`
      (tokens may be 0).
- [ ] Confirm manager-api accepts zero-token payloads (it does — only `mac` is required).
- **Verify:** test session with zero LLM turns → row appears in `device_token_usage_session` with
      duration > 0, tokens = 0.

### 1.3 Track summarization LLM spend
**Problem:** `bridgeSummarizeBatch` (`agent_bridge.go:1731`) calls `provider.Chat` directly —
its tokens are never recorded. Grows exactly when we adopt summary-based context trimming.

- [ ] Call `ab.recordUsage(resp.Usage, elapsed)` after the summarize `Chat` call.
- **Verify:** unit test — session crossing the summarize threshold (20 msgs) records more input
      tokens than the turn-only sum.

### 1.4 Resilience (lower priority)
- [ ] Retry the usage POST once on failure (currently 5s timeout, warn-only → lost billing rows).
- [ ] Optional: periodic mid-session usage heartbeat (e.g. every 5 min) so crashes lose ≤5 min of
      usage and mid-session cutoffs become possible.
- **Verify:** kill worker mid-session → usage row exists with partial duration.

---

## Phase 2 — Per-device limit enforcement (mqtt-gateway + manager-api)

Data source: `device_token_usage_session` (server-measured). Limits are per **calendar day**,
per device; unit = **minutes** (primary) and optionally **tokens** (secondary guard).

### 2.1 Plan/limit storage (manager-api)
- [ ] Add per-device limit fields (e.g. `daily_minutes_limit`, nullable = unlimited) — either on
      `ai_device`/`device_settings` or a new `subscription_plans` table + FK. Decide with schema owner.
- [ ] Endpoint: `GET /device/:mac/usage-today` → `{ usedSeconds, usedTokens, limitSeconds }`
      (sums today's `device_token_usage_session` rows).

### 2.2 Enforcement at session start (mqtt-gateway)
- [ ] In `_deferredSetup` (hello handling, `gateway/mqtt-gateway.js`): before creating the LiveKit
      room, call `usage-today`; if over limit, skip dispatch and send a "time's up" MQTT message to
      the device (kid-friendly TTS-less notice or pre-recorded prompt).
- [ ] Grace behavior: allow the session if remaining > 0 even when it may overshoot (Phase 1.4
      heartbeat later enables hard mid-session cutoff via `end_prompt`).
- **Verify:** device with 30-min cap and 29 min used → session starts; with 31 min used → refused
      with notice; unlimited device unaffected.

### 2.3 Parent app surface (later)
- [ ] Expose usage-today + limit via `/api/mobile` for the parent app; edit limit per kid/device.

---

## Rollout order

| Step | Where | Effort | Why first |
|---|---|---|---|
| 1.1 include_usage + warn | picoclaw | ~1h | Protects billing data through any provider switch |
| 1.2 always-persist duration | picoclaw | ~1h | Makes time limits honest |
| 1.3 summary tracking | picoclaw | ~30m | Correct costing before context-trim work |
| 2.1 + 2.2 limit check | manager-api + gateway | ~1 day | The actual subscription enforcement |
| 1.4 heartbeat/retry | picoclaw | ~½ day | Hard cutoffs + crash-proof billing |

## Out of scope (tracked elsewhere)
- Stack cost changes (TTS/LLM swaps, context trimming) — see `docs/cheeko-costing-sheet.xlsx`
  and its ★ RECOMMENDED / SARVAM scenarios.
- Cosmetic tracking gaps: `avgTtftSeconds` always 0; `input_text_tokens` mirrors `input_tokens`
  (no audio/text split) — harmless, fix opportunistically.
