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

**Open first, added by issue 001:** `ai_agent_template` on the local dev database has **no
`riddler` row** — only `quiz_master`. Establish where Riddler's template actually lives
(DB1, prod, or not a template row at all) before starting; this ticket assumes a Riddler
that this database does not show.

## Acceptance criteria

- [ ] `clearOnReveal` defined per bank in `banks.js`, defaulting to today's behaviour
- [ ] Shared cleared-set logic reads the flag instead of a module-level constant
- [ ] With both banks set to today's values, behaviour is byte-identical to before this change
- [ ] A test proves the two banks diverge: same `revealed` row clears in one bank and not the other
- [ ] Riddler's day gate, level derivation, and age-band handling are untouched
- [ ] `riddle_question_answer` rows are not rewritten

## Blocked by

- 003 — ADR-0009
