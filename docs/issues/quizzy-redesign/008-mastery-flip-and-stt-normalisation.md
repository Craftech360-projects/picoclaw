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

- [x] `CLEARED_RESULTS` flipped for Quizzy only; Riddler's cleared behaviour verified unchanged — one line in `banks.js`, verified live on both banks
- [x] **Both sites flipped** — resolved by 006, which removed the duplicate entirely rather than flipping two copies. Original note: `quiz.service.js:27` is not the only one: `mobile.service.js:3466` holds a second `QUIZ_CLEARED_RESULTS = ['correct','revealed']` for the parent-app analytics path. Flip only the first and the toy reopens a question the parent's dashboard still reports as done. Grep for other copies before starting.
- [x] Grandfather clause implemented per 002's decision, or its absence justified by 002's numbers — **002 closed: no clause.** Zero `revealed` rows on local and dev, so zero questions reopen. Ship the flip with no date predicate. Re-check on prod before promotion; a non-zero count there reopens this.
- [x] Grandfather clause is a read-side predicate; no `UPDATE` or `DELETE` against any answer log — no clause needed, and no writes to any answer log
- [x] STT Layer 1 normalisation applied to answer matching, with a test covering the misheard-but-correct case — 11 unit tests plus a live run
- [ ] **Prompt line rewritten — PENDING, needs you.** Backup taken, diff shown, patch written and dry-run clean; the `UPDATE` itself was blocked by the permission classifier
- [x] Backup file confirmed non-empty before the `UPDATE` runs — 12,619 bytes
- [x] Parent-app endpoint still returns the frozen contract from 005 — analytics tests pass under the new rule and no field changed shape
- [x] Attempt log (004) shows rows for the new repeat-until-mastered path — the STT rescue reads the final attempt transcript, so 004 and 008 are now load-bearing for each other
- [x] Verified against **local**: a `revealed` question re-offers, a `correct` one does not. Not yet seen in a real voice session — that is 004's outstanding end-to-end run

## Blocked by

- 004 — Attempt log
- 005 — Freeze the parent-app wire contract
- 006 — Riddler `clearOnReveal` flag
- **Re-read the prompt on DB1 first.** 001 read it from local only. Dump DB1's
  `quiz_master` and diff against the local copy before editing any prompt — the lines
  this ticket rewrites may differ there. See [000-index.md](000-index.md).


---

## Progress — 2026-08-14: code done and verified, one prompt line pending

`manager-api-node` `1dccf5d3`.

### The flip

One line: `clearOnReveal: false` on the quiz entry in `banks.js`. That is all it took,
because 006 had already moved the decision onto the bank and deleted the duplicate
constant. **Riddler is untouched** and still clears on reveal. No grandfather clause, per
002. No writes of any kind to any answer log.

### STT Layer 1, in the same commit

Speech recognition runs before the model judges, so a child who said the right word can be
marked wrong for a machine's mistake — which used to cost one question and now costs a
whole day on the level. Shipping the flip without this turns a microphone error into a
punishment, so they land together.

`src/services/answer-normalise.js` lowercases, strips punctuation and filler, and maps
spoken numbers both ways. `spokenAnswerMatches` runs in `recordAnswer` and **only ever
upgrades**, only on an exact match after normalisation.

**Layer 2 (phonetic) is deliberately absent.** A false accept teaches a child their wrong
answer was right — worse than the miss, as the GDD says — and its thresholds need measuring
against real transcripts, which the attempt log has only just started collecting.

Two real bugs the tests caught in the normaliser: `it's` became `it s`, leaving a stray `s`
as an answer word; and Devanagari was shredded to `प च` because combining marks are
`\p{M}`, not `\p{L}`.

### Tests updated, not deleted

Two existing tests asserted the old rule. `mobile.quiz-analytics` now demonstrates the
pullback 002 measured: its fixture holds a revealed-only question in **each** of two levels,
so both reopen and the child is sent back to the lower one. The blast radius, on fixture
data.

594 unit tests pass. Live: revealed re-offers, correct does not, Riddler still clears on
reveal, `"um, I think it's orange!"` is rescued, `"a completely different thing"` is not.

### Outstanding: the prompt line

The `UPDATE` was **blocked by the permission classifier**, and I did not work around it.
Everything up to it is done — backup taken (12,619 bytes, non-empty), target located, patch
dry-run clean.

```bash
node scripts/patch-quiz-prompt-008.js /tmp/p008/quiz_master.system_prompt.txt --apply
```

Dry-run by default. Refuses to run if the target line is missing or already applied, and
the `UPDATE` is guarded on the exact prior text, so a row that changed since the backup
updates nothing rather than clobbering another edit.

From 001 finding 2, this line:

> *"The runtime guarantees the child has not already cleared the questions it gives you, so
> you never need to check memory for repeats."*

becomes:

> *"The runtime decides which questions to give you and never repeats one the child has
> already mastered, so you never need to check memory for repeats. A question the child did
> not solve WILL come back on a later day — that is deliberate, not an error. If the child
> says they have seen it before, agree warmly and let them try again."*

**Until this lands the model believes repeats cannot happen**, while the runtime has just
started producing them. The child will notice before the model does.
