# 005 — Freeze the parent-app wire contract

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 1a, §14 action 4a.

## What to build

`GET /toy/api/mobile/progress/quiz` is a **published contract**, documented for app
developers in `manager-api-node/docs/parent-app-quiz-analytics-api.md`. It exposes by
name every field this redesign changes:

- `banks[].age_band` — issue 013 collapses it
- `levels[].correct / wrong / revealed`, documented as *"Three outcomes, not two"*
- `questions[].result`, typed as `"correct" | "wrong" | "revealed"`
- `accuracy` = `correct / attempted`, `points` = `10 × correct`
- an entire section titled *"`revealed` is a third outcome, and it still advances the
  child"*, instructing the app that it needs its own tile and its own icon

So renaming verdicts to `solo | helped | missed` is an **external API break**, not an
internal refactor — and the semantics being reversed are written into the client's
rendering instructions.

Freeze the wire before 008 changes the meaning underneath it:

1. **Map new verdicts to the old three on response** — `solo → correct`,
   `helped → correct`, `missed → revealed`.

   **Corrected by issue 001.** The GDD proposed `helped → revealed`, `missed → wrong`.
   Reading the live prompt showed that is backwards: the MEMO contract emits only
   `correct|revealed`, `revealed` already means *the answer was told to the child after
   two failed tries* — i.e. missed — and Quizzy never emits `wrong` at all. The mapping
   above is the one that keeps a parent's dashboard showing what it shows today.

   Because `helped` and `solo` both map to `correct`, the distinction between them lives
   **only** in the additive `mastery` field below. That is also why the mastery bar
   cannot be backfilled from history — see 001 finding 3.
2. **Add new fields additively** — `door`, `attempts_within_question`,
   `mastery: solo|helped|practised`. Never repurpose an existing field's meaning; an
   app in the wild will keep rendering the old one.
3. **Update the doc's §2 in the same PR.** It currently tells app developers that
   `revealed` advances the child. After 008 that is false, and a parent's dashboard
   will contradict what the toy actually did.
4. **Decide whether `helped` earns partial points.** `accuracy` and `points` shift
   meaning once `revealed` stops clearing. Decide before a parent notices their child's
   score dropped overnight for no visible reason — and write the decision into the doc.

## Acceptance criteria

- [x] Verdict mapping applied on response; a client reading only the old fields sees no change in shape or type
- [x] New fields present and additive; no existing field's meaning repurposed
- [x] `parent-app-quiz-analytics-api.md` §2 rewritten to match post-008 behaviour, in this same change
- [x] Partial-points decision for `helped` made, implemented, and documented — **full points, no change**
- [x] A recorded response from before this change still parses against the documented types — every legacy value passes through untouched, covered by test
- [x] Legacy stored values (`correct`/`wrong`/`revealed`) map correctly on read; history is not rewritten

## Findings — 2026-08-14: done

`manager-api-node` `c1a76cca`. `src/services/mobile.service.js`,
`tests/unit/quiz-wire-contract.test.js`, `docs/parent-app-quiz-analytics-api.md`.

**The seam is `toWireResult`, identity today on purpose.** `solo`/`helped` → `correct`,
`missed` → `revealed`. Nothing maps to a value outside the published enum, and the level
buckets and points now classify by the *wire* value so the three tiles keep their meaning
once the stored vocabulary changes underneath them.

**Partial points: `helped` keeps full points.** `points` is `10 × correct` and `helped`
maps to `correct`, so no score moves. Docking it would drop a child's score overnight for
a change no parent can see — precisely the surprise this ticket exists to prevent.

**`attempts_within_question` and `mastery` are optional and omitted, not defaulted.** A
stored `correct` from before the attempt log conflates first-try and hinted success (001
finding 3), so `mastery` is **null rather than `solo`** when there is no attempt row.
Guessing would overstate a child's mastery to their parent. Absent means *not recorded*,
which is a different fact from *answered first time*.

`door` was **not** added. Nothing can populate it until 009 computes Doors, and a field
that is always absent documents nothing. It belongs to that ticket.

The attempt-count lookup is wrapped: if the attempt log is unavailable the parent's
progress screen still renders, minus the enrichment.

### Verified

9 contract tests, and the full unit suite (55 files, 578 tests) passes.

The first version of this check re-declared the mapping table inside the test rather than
importing it, so it asserted against a copy and would not have failed if the real code
broke. `toWireResult` and `quizMastery` are now exported and the test drives the real
functions.

### Note for 008, confirmed here

`QUIZ_CLEARED_RESULTS` in `mobile.service.js` is genuinely a second copy of the constant,
now commented as such at both the definition and in 008's criteria. It feeds
`loadQuizClearedIds`, which is what decides `levels[].cleared` for the parent dashboard.

## Blocked by

- 003 — ADR-0009
