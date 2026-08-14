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

- [x] Door computed server-side per question and returned in the next-questions payload — `ask_mode`, `attempt_no`, plus `choice_order` and `teach_text` where authored
- [x] Door derives from server state; no client or model input decides it — **but not from logged attempts**, see the deviation below
- [x] Door 1 or Door 2 unaided marks the question cleared; Door 3 does not — needs **no schema change**, see below
- [x] A previously-missed question reopens at Door 1 on a later day — every question does, which is why nothing here reads history
- [ ] **Spaced-repetition items flagged bonus-only — NOT DONE.** No spaced-repetition pool exists yet; there is nothing to flag until one is built. Carried to 012, which owns the pool
- [x] A question with no prior attempts always returns Door 1
- [x] Attempt-log write failure does not corrupt Door computation — **structurally impossible now**: the Door never reads the attempt log
- [x] Response remains backward-compatible with the frozen contract from 005 — every new field is additive and the existing four are untouched

## Blocked by

- 004 — Attempt log
- 008 — `CLEARED_RESULTS = ['correct']` + STT Layer 1 normalisation


---

## Progress — 2026-08-14: payload done, one deviation and one item carried

`manager-api-node` `b3ba2114`. 605 unit tests pass; verified live.

### The deviation you should know about

quizzy-doors.md resolves the circular dependency as **"the server computes
`attempt_no` and assigns `ask_mode` before the turn, from rows already written; the log
records what was assigned after the turn resolves. Read before, write after."**

That does not fit what 004 shipped. The worker writes attempts **when a question
resolves**, bundled with the answer — so during a sitting there are no rows to read, and
honouring the contract literally would mean an HTTP round trip mid-question, on every
escalation, on a voice path. That pause is the child wondering whether the toy broke.

**What I did instead: the whole ladder ships at fetch.** Each question carries
`ask_mode` (the starting Door), `attempt_no`, and the authored content for Doors 2 and 3.
The worker escalates through what it was handed.

The intent behind the rule survives — **the model still chooses nothing.** The distractor
and the teaching sentence are authored, the option order is decided server-side, and the
ladder is fixed. What moved is *when* the data is delivered, not who decides it.

If per-turn assignment is wanted anyway, the change is 004's worker posting each attempt
as it happens rather than bundling — which is a decision about voice latency, not about
correctness. **Flagged for 010, which owns the escalation.**

### The mastery bar needs no schema change

"Door 1 or 2 unaided clears; Door 3 does not" looked like it needed a `door` column on the
answer row. It does not.

Door 3 **is** the guided path — the child is walked to the answer. Reporting that as
`revealed` is semantically exact, and after 008 `revealed` no longer clears. So:

- Door 1 or 2 success → `correct` → clears
- Door 3 success → `revealed` → does not clear
- No answer → `revealed` or `wrong` → does not clear

The rule falls out of vocabulary that already exists. **010 implements the reporting side**,
since the worker is what knows which Door produced the answer.

### `choice_order` is stable per question

Seeded by question id, so a child hears the same order every time. A shuffle that moved
between days would teach position rather than the answer — "it was the second one" is right
on Monday and wrong on Tuesday.

### Unauthored Doors are omitted, not empty

`choice_order` and `teach_text` are absent unless authored, so the worker can tell "no Door
2 for this question" from "Door 2 with no options" and skip the rung rather than improvise
one. **That is the state of the entire bank today** — the live run confirms 0 of 10 served
questions have either, and will stay that way until 014 authors them.

### Carried to 012: spaced repetition

The bonus-only flag has nothing to attach to — no spaced-repetition pool exists. 012 owns
building it as a query, and should carry the never-blocks rule with it.
