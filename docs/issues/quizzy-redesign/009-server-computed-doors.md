# 009 — Server computes `ask_mode` / Door per question

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 3 (M1, M2),
[quizzy-doors.md](../../design/quizzy-doors.md).

## What to build

The Three Doors escalation: a question is asked plainly (Door 1), then with a narrowing
hint (Door 2), then guided (Door 3). Which Door applies is computed **server-side from
prior attempts** and handed to the worker.

**The model must not choose the Door.** ADR-0005's lesson applies directly, and §11
restates it: Doors, verdicts, mastery, streaks and the day gate are all computed
server-side and handed to the model as lines to say. A model asked to track which Door
it is on will drift, and the child's mastery record drifts with it.

Compute from the attempt log (004) plus the outcome log, and return the Door alongside
each question in the next-questions payload. The worker consumes it in 010.

Locked design decisions this must implement:

- One attempt per Door; three Doors available in one sitting
- Door 1 **or** Door 2 unaided clears the question (the mastery bar)
- A repeat of a previously-missed question **reopens at Door 1**, not where it left off
- Spaced-repetition items are bonus-only and never block progression

Add the spaced-repetition pool as a **query, not a table**, if you can (§10 Step 4).

## Acceptance criteria

- [ ] Door computed server-side per question and returned in the next-questions payload
- [ ] Door derives from logged attempts; no client or model input decides it
- [ ] Door 1 or Door 2 unaided marks the question cleared; Door 3 does not
- [ ] A previously-missed question reopens at Door 1 on a later day
- [ ] Spaced-repetition items are flagged bonus-only and never gate a level
- [ ] A question with no prior attempts always returns Door 1
- [ ] Attempt-log write failure does not corrupt Door computation — behaviour on missing attempts is defined and tested
- [ ] Response remains backward-compatible with the frozen contract from 005 (`door` is additive)

## Blocked by

- 004 — Attempt log
- 008 — `CLEARED_RESULTS = ['correct']` + STT Layer 1 normalisation
