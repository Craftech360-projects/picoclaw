# 004 — Attempt log: table, write path, read-back

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §6a, §14 action 3.

## What to build

Today the database records one row per *question outcome*. It does not record how many
tries it took, which Door the child was on, or what they actually said. Every mechanic
in this redesign needs that, and the mastery rule (008) is unsafe without it.

**This ships before the mastery rule, deliberately.** Once `revealed` stops clearing a
question, every mis-scored answer costs a child an entire day. Ship enforcement first
and the earliest signal that mastery is hurting a child is a parent complaint — with no
data showing which questions stall, how many tries they take, or how often STT was the
real culprit. Log first, then enforce.

One new table (§6a), written from the existing answer-reporting path, and readable
end-to-end. A thin vertical slice: schema → service write → worker reports attempts →
verifiable read-back.

Two rules that are load-bearing:

- **The attempt log is additive.** It never replaces `quiz_question_answer`, and the
  outcome row keeps being written exactly as today. Anything derived for gating —
  `days_on_level`, streak, cleared set — derives from `quiz_question_answer`, **not**
  from this log. The attempt write may fail; if a gate depended on it, the anti-trap
  cap (012) would silently never fire.
- **No reward or attempt state in `memory/state/`** — the 48h prune
  ([quiz_state.go:26](../../../pkg/livekit/quiz_state.go)) deletes it (§6a-2).

## Acceptance criteria

- [ ] Migration adds the attempt table per §6a, with an index supporting per-question and per-kid lookups
- [ ] An attempt row is written for every try, including wrong ones that never become an outcome row
- [ ] Each row carries at minimum: question, kid, attempt ordinal, verdict, and the raw transcribed answer
- [ ] `quiz_question_answer` write behaviour is unchanged — verified by existing tests still passing
- [ ] An attempt-write failure does not fail the answer submission or block the child's turn
- [ ] Read-back proven: a query returns the attempt sequence for a single question in order
- [ ] Nothing added under `memory/state/`
- [ ] Verified end-to-end against a real session, not only unit tests

## Blocked by

- 003 — ADR-0009
