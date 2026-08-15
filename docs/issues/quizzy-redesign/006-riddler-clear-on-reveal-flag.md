# 006 — Riddler `clearOnReveal` flag

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 1a, §13 Q0, §14 action 4.

## What to build

`banks.js` `resolveBank` gives quiz and riddle **one shared service**. `CLEARED_RESULTS`,
`deriveLevelState`, `ageBandFromBirthDate` and the day gate are all shared code. So the
one-line change in issue 008 **changes Riddler automatically, whether you intend it or
not.**

That is probably wrong for riddles. A riddle whose answer you already know isn't a
riddle — repeating it until solved teaches nothing and is just tedious. Quizzy wants
mastery; Riddler wants flow.

Add a per-bank `clearOnReveal` flag in `banks.js` — that file is already the right home
for exactly this kind of per-bank difference — and have the shared cleared-set logic
consult it instead of a module-level constant.

**Ship this before 008, not with it.** Landing it first means the mastery flip is a
one-line change to one bank's config rather than a change that silently alters a second
character's behaviour in the same commit.

Riddler keeps today's behaviour: `clearOnReveal: true`. Quizzy flips to `false` in 008.

**Resolved — the row is `riddle_master`.** Issue 001 reported no `riddler` row, and that
was a query bug, not a missing character. Listing `ai_agent_template` shows eight
characters, including `agent_code = 'riddle_master'` with `agent_name = 'riddler'` —
**exactly the same trap as `quiz_master`/`quizzy`**, and the GDD's own SQL repeats it by
querying `IN ('quiz_master','riddler')`. Riddler exists and is registered in
`banks.js CHARACTER_BANK` under `riddle_master`.

## Acceptance criteria

- [x] `clearOnReveal` defined per bank in `banks.js`, defaulting to today's behaviour
- [x] Shared cleared-set logic reads the flag instead of a module-level constant — **all four sites**, across two services
- [x] With both banks set to today's values, behaviour is byte-identical to before this change — 583 unit tests pass, and both banks serve and score identically against the real database
- [x] A test proves the two banks diverge: same `revealed` row clears in one bank and not the other
- [x] Riddler's day gate, level derivation, and age-band handling are untouched
- [x] `riddle_question_answer` rows are not rewritten — no writes of any kind in this change

## Findings — 2026-08-14: done

`manager-api-node` `aa54a6ef`. `banks.js`, `quiz.service.js`, `mobile.service.js`,
`tests/unit/bank-clear-on-reveal.test.js`.

**`clearedResultsFor(bank)` is the single answer to "what clears".** Both module-level
constants are gone — `CLEARED_RESULTS` in `quiz.service.js` and `QUIZ_CLEARED_RESULTS` in
`mobile.service.js`. That second copy is the one 002 flagged: had 008 flipped only the
first, the parent dashboard would have kept reporting a question as done while the toy
reopened it. There is now one place to change and no way for the two to disagree.

`ANSWER_RESULTS` stays global, deliberately: what the worker may *report* is the same for
both banks, and only what *clears* differs.

**Four call sites, not two.** `loadClearedIds` and `allDeviceProgress` in `quiz.service.js`;
`loadQuizClearedIds` and the replay-detection query in `mobile.service.js`.

**A bank with no flag does not clear on reveal.** Fail safe towards mastery: a new bank
that forgot to declare its intent should repeat an unsolved question rather than hand out
a free pass. A test also asserts every registered bank states its intent explicitly, so
that default stays a safety net rather than somewhere a real bank lands.

**Nothing changes behaviourally yet.** Both banks are `true`. 008 flips quiz alone.

### Verified

- 583 unit tests pass (56 suites), including 6 new ones proving the banks *can* diverge
- Live run against the real database: both banks serve 10 questions at level 1 and report
  the same progress as before

One bug caught on the way, mine: the first edit referenced a `cleared` variable in
`allDeviceProgress` without defining it, which four existing tests caught immediately.
`clearedResultsFor` is now resolved once per page rather than per device.

### Note for 008

The `clearOnReveal: true` on the **quiz** entry in `banks.js` is the line to flip. Nothing
else in either service needs touching for the cleared-set half of that ticket.

## Blocked by

- 003 — ADR-0009
