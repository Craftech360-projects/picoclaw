---
id: SUB-4
title: "Honest metering — tracking holes 1.1–1.3"
type: AFK
status: open
triage: afk-ready
blocked-by: []
---

## Parent

`docs/plan-usage-tracking-and-limits.md` Phase 1 (1.1–1.3); spec §8 phase 0.

## What to build

Close the three usage-tracking holes in the picoclaw worker so the meters the subscription bills against are honest: (1) request `stream_options: {"include_usage": true}` in openai-compat streaming and WARN when usage arrives nil; (2) always persist the session summary when duration > 0, even with zero tokens (silent kids must still consume trial days and minute caps); (3) record the summarization batch's LLM spend through the same `recordUsage` path as normal turns.

Independent of all other slices — pure worker correctness work.

## Acceptance criteria

- [ ] Streamed completion via OpenAI-direct reports non-zero input/output tokens in the usage POST
- [ ] Usage response absent ⇒ WARN log naming provider/model
- [ ] A connect-and-say-nothing session produces a `device_token_usage_session` row with duration > 0, tokens = 0
- [ ] A session crossing the summarize threshold records more input tokens than the sum of its turns alone
- [ ] Existing `go test ./...` remains green

## Blocked by

None — can start immediately.
