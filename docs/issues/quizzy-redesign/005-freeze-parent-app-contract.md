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

- [ ] Verdict mapping applied on response; a client reading only the old fields sees no change in shape or type
- [ ] New fields present and additive; no existing field's meaning repurposed
- [ ] `parent-app-quiz-analytics-api.md` §2 rewritten to match post-008 behaviour, in this same change
- [ ] Partial-points decision for `helped` made, implemented, and documented
- [ ] A recorded response from before this change still parses against the documented types
- [ ] Legacy stored values (`correct`/`wrong`/`revealed`) map correctly on read; history is not rewritten

## Blocked by

- 003 — ADR-0009
