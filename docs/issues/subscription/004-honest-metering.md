---
id: SUB-4
title: "Honest metering — tracking holes 1.1–1.3"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: []
---

## Parent

`docs/plan-usage-tracking-and-limits.md` Phase 1 (1.1–1.3); spec §8 phase 0.

## What to build

Close the three usage-tracking holes in the picoclaw worker so the meters the subscription bills against are honest: (1) request `stream_options: {"include_usage": true}` in openai-compat streaming and WARN when usage arrives nil; (2) always persist the session summary when duration > 0, even with zero tokens (silent kids must still consume trial days and minute caps); (3) record the summarization batch's LLM spend through the same `recordUsage` path as normal turns.

Independent of all other slices — pure worker correctness work.

## Acceptance criteria

- [x] Streamed completion via OpenAI-direct reports non-zero input/output tokens in the usage POST
- [x] Usage response absent ⇒ WARN log naming provider/model
- [x] A connect-and-say-nothing session produces a `device_token_usage_session` row with duration > 0, tokens = 0
- [x] A session crossing the summarize threshold records more input tokens than the sum of its turns alone
- [x] Existing `go test ./...` remains green

## Blocked by

None — can start immediately.

## Resolution (picoclaw@876c449, 2026-07-17)

All three holes closed; the review loop reshaped the fix well beyond the plan's
one-liners:

- **1.1** `stream_options: {include_usage: true}` in ChatStream — set only when
  absent so a per-model `extra_body` override remains the escape hatch for any
  backend that rejects the field (buildRequestBody merges extra_body last as
  the documented precedence; setting it unconditionally after was a clobber).
  Nil usage WARNs **once per session** (it fires per LLM round; a provider that
  never reports usage would have spammed every turn). Criterion 1's
  "OpenAI-direct" as written is unverifiable here — no OpenAI-direct key exists
  in `llm_providers` — but the change was **live-verified against both real DB
  providers**: OpenRouter gemma (prompt=20/completion=2) and Groq qwen3
  (prompt=15/completion=337) streamed non-zero usage through the new code, and
  Groq doubles as evidence a second real backend accepts stream_options.
- **1.2** Silent sessions POST their usage row (verified against the running
  manager-api: zero-token payload → real `device_token_usage_session` row,
  duration 42.5, then deleted) but now take an early branch that skips
  summary/chat/end/trace. Review caught that relaxing the old guard naively
  would have run a **paid finalize-summarize LLM call over the device's
  persisted history on every silent reconnect**, appended duplicate MEMORY.md
  entries, and written an unbounded trace file per room. `sendUsageSummary`
  lost its own guard — the caller owns billing policy in exactly one place.
- **1.3** `recordSummarizeUsage` meters tokens **without** bumping
  messageCount/totalResponseDurationSecond (manager treats those as
  conversational-turn metrics — device.service.js weights TTFT and bumps
  session_count off them). Review's biggest catch: the end-of-session finalize
  ran AFTER the usage POST, so the very tokens this ticket set out to meter
  were recorded into a struct nobody read again. persistPostSessionData now
  finalizes first, re-snapshots, and bills — while keeping the pre-finalize
  duration so post-session LLM latency isn't billed as session time.
- **Criterion 5 honestly**: `go test ./...` was NOT green before this ticket on
  this machine (cgo toolchain failures in go-sqlite3/libolm/vad + ~14
  environmental test failures, all reproduced on the clean tree). Verified via
  full-suite failure-set diff: the change introduces zero new failures; both
  touched packages pass everything except the pre-existing
  TestSynthesizeAndPlayLogsTTSProviderType (fails on clean tree in isolation).

Residual (noted, out of scope):
- An in-flight `maybeSummarize` goroutine that completes after the final
  snapshot still goes unbilled, and can race FinalizeSessionSummary into a
  duplicate summarize call — **pre-existing** (before this ticket those tokens
  were never billed at all); worth folding into SUB-5's mid-session heartbeat
  work if it touches this path.
- A failed room join still constructs a bridge, so join-failure retry loops
  POST small duration-only rows (sessionStart is set in the constructor). Rows
  are sub-second-to-seconds each; if it shows up in real metering, gate
  persistence on a successful join.
- Tests for the WARN and silent-session paths build `&AgentBridge{}` literals
  directly (the constructor needs full config); acceptable in-package but they
  bypass constructor invariants.
