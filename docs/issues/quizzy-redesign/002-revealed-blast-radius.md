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

---

## Findings — 2026-08-14 (local run: measurement inconclusive, script ready)

Ran against the **local dev** database (`shlrfpbqkfnxqcmuatvs`). Read-only.

**The local database cannot answer this ticket.** It holds 27 quiz answers across 3
devices and 1 riddle answer across 1 device — and **every single row is `correct`**.
Zero `revealed`, zero `wrong`, so zero questions reopen and zero devices are pulled back.

That is not the answer "the blast radius is zero." It is "this database has never seen a
child fail a question twice." **The go/no-go on the grandfather clause is still open**
and must come from DB1 and prod, where real children have been playing.

What the local run did establish:

- **The script is correct and safe to point at DB1.** Now committed at
  `manager-api-node/scripts/quiz-revealed-blast-radius.js`. Read-only, no writes of any
  kind. It reports the verdict spread, the case-folded device count, per-device level
  before/after, and worst/median pullback for both banks.
- **Weak corroboration of 001 finding 3.** Zero `wrong` rows in either bank is consistent
  with the live prompt being unable to emit `wrong` — but n=27 is far too small to
  conclude anything. Re-check on DB1, where the count is meaningful.
- **MAC case-folding is handled.** `mobile.service.js` documents that the dev database
  holds both `00:16:3E:AC:B5:38` and `00:16:3e:ac:b5:38` for one device — 10 rows against
  16 — and that a case-sensitive match silently reports the smaller half. The script folds
  case and prints raw vs folded device counts so the split is visible. Locally they match
  (3 and 3); on DB1 they may not, and an unfolded run would **understate** the pullback.

### Found while writing the query: `CLEARED_RESULTS` has a second site

`mobile.service.js:3466` defines its own `QUIZ_CLEARED_RESULTS = ['correct', 'revealed']`
for the parent-app analytics path, separate from the one in `quiz.service.js:27` that
GDD §10 Step 1 names.

**Flipping only the service constant leaves the parent app still counting `revealed` as
cleared** — the toy would reopen a question the parent's dashboard reports as done.
Recorded as an acceptance criterion on 008.

## Findings — 2026-08-14, second run (DB1 data copied to local)

Copied DB1's bank and answer log into local via
`manager-api-node/scripts/copy-quiz-tables.js`, then re-ran the measurement.

**DB1 cannot answer this ticket either.** It holds 45 quiz answers across 5 devices and
10 riddle answers across 1 — and, exactly like local, **every row is `correct`**.

| | local (before) | DB1 → local (now) |
|---|---|---|
| quiz questions | — | 330 |
| quiz answers | 27 | 45, all `correct` |
| riddle answers | 1 | 10, all `correct` |
| devices | 3 | 5 |
| `revealed` | 0 | 0 |
| `wrong` | 0 | 0 |
| pullback | 0 levels | 0 levels |

**55 answer rows across two databases and not one `revealed`, not one `wrong`.**

The write path is not the explanation — it accepts all three. `quiz.service.js:26`
validates against `ANSWER_RESULTS = ['correct','wrong','revealed']` and rejects anything
else with a 400; the worker's `quizVerdictResults` ([quiz_state.go:247](../../../pkg/livekit/quiz_state.go))
accepts the same three. Both ends can carry `revealed` today.

The likeliest explanation is mundane: 9 answers per device across 5 devices is staff
smoke-testing, and a tester answers their own quiz correctly. This is test data, not
child data. But it is worth one look at prod before concluding that — if prod also shows
zero `revealed` across real children, the reveal path is not firing and **requirement 4
reverses a decision that never takes effect**, which would change what this whole redesign
is for.

### Still open: only prod can close this

Neither local nor DB1 has ever recorded a child failing a question twice. The
grandfather-clause decision needs prod, which no dev session touches without being asked.

The measurement is read-only — two `SELECT`s and no writes — so it is safe to point at
prod, but that is a decision to take deliberately, not a side effect of this ticket.

```bash
node scripts/quiz-revealed-blast-radius.js
```

Then fill in the criteria above from real numbers and make the grandfather-clause call.
