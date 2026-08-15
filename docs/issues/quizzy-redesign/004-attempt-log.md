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

## Progress — 2026-08-14: API half done and verified, worker half outstanding

**How per-try rows are captured, settled.** The ticket asked for a row per try including
wrong ones, but `NewQuizAnswerReporter` posts `(questionID, result)` **once per finished
question**, and the prompt explicitly says to omit `scored_q`/`result` on turns that did
not finish judging. So intermediate tries never leave the worker today.

This does **not** need a prompt change. The worker already sees every child turn and every
MEMO, and `awaiting=<id>` stays on the same question across a retry — so each turn while
`awaiting` is unchanged *is* a failed attempt, with the transcript already in hand. The
worker accumulates the sequence and sends it with the final answer. One write per question,
nothing new on the conversation's critical path, and no edit to a prompt that 008 and 010
are about to rewrite anyway.

### Done (manager-api-node, uncommitted)

- `prisma/schema.prisma` — `question_attempt` model
- `prisma/migrations/20260814000000_question_attempt_log/migration.sql`
- `src/services/quiz.service.js` — `recordAttempts`, called from `recordAnswer` inside the
  same try/catch pattern as `recordLevelMilestone`
- `src/routes/quiz.routes.js` — optional `attempts[]` on `POST /quiz/answer`, plus swagger

**One table for both banks**, with a `bank` discriminator and no FK, rather than the
parallel pair the answer logs use. Affordable precisely because ADR-0009 makes these rows
non-authoritative: an orphaned attempt row is a lost measurement, not a child's lost
progress.

**Ordinals are assigned server-side** from array position rather than taken from the
caller. The worker counts turns; letting it also name the ordinal would let a retry or a
dropped turn write attempt 3 twice.

### Verified against local

Four assertions, all passing, test rows deleted afterwards:

1. Two attempts written and read back in order, ordinals 1 and 2; an unfinished try
   defaults to `wrong`
2. A whitespace-only transcript stores as `null` — silence and a mangled word are
   different facts and must stay distinguishable
3. **An attempt-write failure did not fail the answer.** Forced a real database error
   (over-long `verdict` against `VARCHAR(10)`) and confirmed the answer row was still
   written and returned
4. A worker sending no `attempts` behaves exactly as today

### Worker half — done (picoclaw, committed)

`pkg/livekit/agent_bridge.go`, `pkg/livekit/quiz_bank.go`, `pkg/livekit/quiz_attempt_test.go`.

`trackQuizAttemptLocked` folds each finished reply into the pending question's list and
returns the completed list when that question is judged. `QuizAnswerReporter` and
`PostQuizAnswer` take the attempts; the field is omitted when empty, so a first-time
correct answer posts exactly the bytes it posted before.

Three judgement calls worth knowing about:

- **Silence is not an attempt.** The prompt already says silence does not count as a try,
  and a row for it would make a quiet child read as a struggling one.
- **A verdict for an untracked question reports only its final try.** `parseQuizVerdict`
  corrects a stray id to the only pending question, so a verdict can arrive for a question
  whose earlier turns were never tracked. Attaching another question's tries to it would be
  worse than losing them.
- **The list is capped** (`maxTrackedAttempts = 10`) so a looping session cannot grow it
  without bound. Three Doors means three tries by design; the cap is only a ceiling.

Six test cases cover the sequences a real Daily Ten produces: two tries then correct,
first-time correct, silence, the untracked-question case, the cap, and a non-quiet
character emitting no quiz MEMO at all. `go vet` clean, package tests pass.

**One pre-existing failure in the suite**, unrelated: `TestSynthesizeAndPlayLogsTTSProviderType`
fails identically with these changes stashed.

### Closed 2026-08-14 — verified in real sessions

Device `68:EE:8F:60:BC:00`, local. 14 attempt rows across 6 questions, both paths proven.

**Path A — the question resolves.** Attempts travel with the verdict.

| q | tries | outcome |
|---|---|---|
| 229 | "water" → "sugar" → "jaggery" | `revealed` after two misses |
| 232 | "Can you repeat the question?" → "It is 12." | `correct` |
| 217, 220, 223 | one try each | `correct` first time |

**Path B — the session ends mid-question.** Q226 was abandoned after five tries: all five
rows written in one flush at teardown, **no answer row**. Before the fix this produced
nothing at all. The question stayed unmastered, was re-offered next session, and was
answered correctly — the mastery loop end to end.

Three bugs were found by these sessions and fixed, none of which unit tests had caught:

1. **The Door directive suppressed the prompt's own escalation.** It said "do not hint yet"
   every turn on a question with no authored ladder, so a child looped eight times with no
   hint, no answer and no score.
2. **The model was never told which try it was on.** `answered`/`first_try`/`missed` are day
   totals; the per-question count lived only in chat history, which is summarised away
   mid-question. The worker had the count exactly and now hands it back.
3. **A clarification request counted as a wrong answer.** "Can you repeat the question?"
   became a miss, and two misses trip the reveal. Stopgap filter shipped; proper fix is
   [016](016-report-unclear-turns.md).

### Two things found, neither in scope here

- **Migration ledger drift on local.** Six migrations are recorded in the database but
  absent from `prisma/migrations`, so `prisma migrate deploy` refuses to run *anything*.
  Applied this migration with `prisma db execute` instead; it is `IF NOT EXISTS`, so a
  later successful `deploy` re-runs it harmlessly. **This matters for the dev box**, where
  `server.js` runs `runPrismaMigrations()` on boot — if DB1 has the same drift, this
  migration will not auto-apply on restart. Check before deploying.
- **Transcript retention is unaligned with ADR-0006.** That ADR expires raw transcripts
  while durable memory persists. `question_attempt.transcript` is raw child speech with no
  expiry, which quietly creates a second, permanent home for exactly what ADR-0006 chose to
  expire. Needs a retention decision before this reaches production.

## Acceptance criteria

- [x] Migration adds the attempt table per §6a, with an index supporting per-question and per-kid lookups
- [x] An attempt row is written for every try, including wrong ones that never become an outcome row — assembled by the worker from `awaiting=`, no prompt change
- [x] Each row carries at minimum: question, kid, attempt ordinal, verdict, and the raw transcribed answer
- [x] `quiz_question_answer` write behaviour is unchanged — existing Go tests updated only for the new signature; the one suite failure is pre-existing and unrelated
- [x] An attempt-write failure does not fail the answer submission or block the child's turn — proven with a forced database error, not by inspection
- [x] Read-back proven: a query returns the attempt sequence for a single question in order
- [x] Nothing added under `memory/state/`
- [x] **Verified end-to-end against a real session** — 2026-08-14, device `68:EE:8F:60:BC:00`. Both paths, real transcripts. See below

## Blocked by

- 003 — ADR-0009
