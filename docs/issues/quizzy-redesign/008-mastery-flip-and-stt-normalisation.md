# 008 — `CLEARED_RESULTS = ['correct']` + STT Layer 1 normalisation

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 1, §6b, §14 action 5.

## What to build

The mastery reversal. `manager-api-node/src/services/quiz.service.js:27`:

```js
const CLEARED_RESULTS = ['correct', 'revealed'];   // before
const CLEARED_RESULTS = ['correct'];               // after
```

Governed by ADR-0009 (003). Scoped to Quizzy only, via the `clearOnReveal` flag from
006 — Riddler keeps flow.

**Ship the STT normalisation in the same change, not after.** Once a revealed answer no
longer clears a question, a misheard answer costs the child an entire day. The failure
mode this creates is a child who answered correctly, was misheard, and is now stuck
repeating a question they have already mastered. Layer 1 normalisation (§6b) is what
keeps that from being the common case, so the enforcement and the mitigation land
together or not at all.

Apply the grandfather decision from 002. If the pullback is 5+ levels, add a
date-predicate grandfather clause in `loadClearedIds` treating pre-cutover `revealed`
rows as still cleared. **Never rewrite the answer log** — the clause is a read-side
predicate, not a migration.

Prompt changes go through ticket 006's backup-and-show-the-diff procedure. Issue 001
identified which prompt lines hardcode `revealed` semantics in the MEMO instruction;
those are the lines to rewrite.

## Acceptance criteria

- [ ] `CLEARED_RESULTS` flipped for Quizzy only; Riddler's cleared behaviour verified unchanged
- [ ] **Both sites flipped** — added by issue 002. `quiz.service.js:27` is not the only one: `mobile.service.js:3466` holds a second `QUIZ_CLEARED_RESULTS = ['correct','revealed']` for the parent-app analytics path. Flip only the first and the toy reopens a question the parent's dashboard still reports as done. Grep for other copies before starting.
- [ ] Grandfather clause implemented per 002's decision, or its absence justified by 002's numbers
- [ ] Grandfather clause is a read-side predicate; no `UPDATE` or `DELETE` against any answer log
- [ ] STT Layer 1 normalisation applied to answer matching, with a test covering the misheard-but-correct case
- [ ] Prompt lines hardcoding `revealed` semantics rewritten; backup taken and diff shown before the `UPDATE`
- [ ] Backup file confirmed non-empty before the `UPDATE` runs
- [ ] Parent-app endpoint still returns the frozen contract from 005 — verified against a recorded response
- [ ] Attempt log (004) shows rows for the new repeat-until-mastered path
- [ ] Verified against a real session: a `revealed` question re-offers, a `correct` one does not

## Blocked by

- 004 — Attempt log
- 005 — Freeze the parent-app wire contract
- 006 — Riddler `clearOnReveal` flag
- **Re-read the prompt on DB1 first.** 001 read it from local only. Dump DB1's
  `quiz_master` and diff against the local copy before editing any prompt — the lines
  this ticket rewrites may differ there. See [000-index.md](000-index.md).
