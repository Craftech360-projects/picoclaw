# 005 — Verdict reporting: MEMO q=/result= → answer log

**Type:** AFK · **Status:** ready
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

- 003 (answer endpoint), 004 (batch + reporter plumbing on the bridge)
