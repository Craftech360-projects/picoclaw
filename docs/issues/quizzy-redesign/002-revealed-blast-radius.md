# 002 — Measure the `revealed` level-pullback blast radius

**Type:** HITL · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 1, §13 Q2, §14 action 2.

## What to build

Issue 008 flips `CLEARED_RESULTS` so `revealed` no longer clears a question. Every
existing `revealed`-only row then reopens at once, pulling children backwards through
levels they have already passed. Measure that before shipping the flip.

**The number that matters is not the row count — it is the level pullback.** One
revealed-only row sitting in level 2 yanks a child who is now on level 9 all the way
back to level 2. Count per child, not per row.

Exclude `(device, question)` pairs that also have a `correct` row — those are already
cleared and reopen nothing.

Run the same measurement against `riddle_question_answer`. It shares the code path
(`banks.js` `resolveBank`) and inherits the change automatically.

**The decision this produces:**

- Worst case 1–2 levels → ship 008 with no grandfather clause.
- Worst case 5+ levels → 008 must add a date-predicate grandfather clause in
  `loadClearedIds`, treating rows before the cutover date as still-cleared.

**Never rewrite the answer log.** The grandfather clause is a read-side predicate, not
a data migration. The log is the evidence base for issue 004 and for every later
question about whether mastery is hurting a child.

## Acceptance criteria

- [ ] Count of `revealed` rows with no matching `correct` row for the same `(device, question)`
- [ ] Count of `wrong` rows per character — **added by issue 001**: the live Quizzy prompt emits only `correct|revealed` and cannot produce `wrong`, so any Quizzy `wrong` row was written by something else and needs explaining before 008
- [ ] Per-child level pullback computed: current level vs level they would fall back to
- [ ] Worst-case and median pullback reported in levels, for both `quiz_question_answer` and `riddle_question_answer`
- [ ] Explicit go/no-go on the grandfather clause, with the cutover date if needed
- [ ] Queries recorded in a comment so the measurement can be re-run after the cutover
- [ ] No `UPDATE` or `DELETE` executed under this issue

## Blocked by

None - can start immediately. Needs Manager DB access.
