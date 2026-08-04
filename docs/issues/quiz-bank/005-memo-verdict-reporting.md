# 005 — Verdict reporting: MEMO q=/result= → answer log

**Type:** AFK · **Status:** closed (dev-box E2E deferred to 006)
**Spec / Plan:** as 001 (plan Task 7)
**Repo:** picoclaw

## What to build

The verdict path end to end: the daily-quiz `MEMO:` grammar gains `q=<id> | result=correct|wrong|revealed` per scored judgment; the worker parses it on the existing MEMO channel (already TTS-hidden and truncation-guarded), validates, and POSTs to 003's answer endpoint in real time.

Validation rules (the contract — from the grill session):

- Only `type=daily_quiz` MEMOs are considered; `type=story` etc. never report.
- `q` must be an id from the injected batch; unknown id → reject with a warning (no garbage POSTs), **unless exactly one batch question is still unreported — then correct the id to it** (the 31B model echoes ids imperfectly).
- Duplicate report for an already-reported id in the same session → reject.
- Nil batch (fetch failed) → never report.
- POST is async with one retry; final failure is log-and-drop (the DB self-heals next session: unreported = uncleared = re-asked).

## Acceptance criteria

- [ ] Table-driven Go tests beside the existing quiz-state tests cover: valid correct/revealed, missing q, bad result, unknown id with 2+ pending (reject), unknown id with exactly 1 pending (corrected), duplicate (reject), story-MEMO (ignored), nil batch
- [ ] `go build ./...` clean
- [ ] On the dev box, a real session judgment produces a `quiz_question_answer` row whose result matches the transcript
- [ ] Committed on the picoclaw branch

## Blocked by

- 003 (answer endpoint), 004 (batch + reporter plumbing on the bridge) — both closed

## Resolution

Shipped in `4ecefab` (picoclaw). `parseQuizVerdict` covers all 18 table cases: valid
correct/wrong/revealed, tolerated result casing, missing/empty/non-numeric `q`, missing
or invalid `result`, unknown id with 2+ pending (reject), unknown id with exactly 1
pending (corrected), unknown id with 0 pending (reject), duplicate (reject), `type=story`
MEMO (never reports), missing type, nil batch, empty batch, nil reported map.

**Concurrency fix beyond the plan:** the check-and-mark of `reportedQuizIDs` is guarded
by a mutex. `runIterationWithProfile` is reachable from three concurrent call sites
(conversation, proactive, async-tool continuation) and `sessionLLMLocks` only serialises
per session key, so an unguarded map write could panic the worker. The lock covers only
the pure parse, never the HTTP POST, which stays async so a voice turn is never blocked.

`go test ./pkg/livekit/` shows only the known pre-existing
`TestSynthesizeAndPlayLogsTTSProviderType` failure (re-confirmed by stashing).
Live-session verification deferred to SUB-006.
