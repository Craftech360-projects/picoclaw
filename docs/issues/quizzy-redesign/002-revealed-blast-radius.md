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

Measured on **local and dev (DB1)**. Prod deliberately out of scope — see the decision below.

- [x] Count of `revealed` rows with no matching `correct` row for the same `(device, question)` — **0** on both banks, both databases
- [x] Count of `wrong` rows per character — **0** on both banks, both databases. Consistent with issue 001's finding that the live prompt emits only `correct|revealed`, though the sample is too small to call it proof
- [x] Per-child level pullback computed: current level vs level they would fall back to — **0 devices affected** of 5 (quiz) and 1 (riddle)
- [x] Worst-case and median pullback reported in levels, for both `quiz_question_answer` and `riddle_question_answer` — **worst 0, median 0**, both tables
- [x] Explicit go/no-go on the grandfather clause, with the cutover date if needed — **no clause needed for local or dev**; prod undecided, see below
- [x] Queries recorded so the measurement can be re-run after the cutover — committed as `manager-api-node/scripts/quiz-revealed-blast-radius.js`
- [x] No `UPDATE` or `DELETE` executed under this issue — the measurement is two `SELECT`s per bank and writes nothing. (Separately, the DB1→local refresh **did** replace local's four quiz/riddle tables. That was a deliberate data refresh via `copy-quiz-tables.js`, not part of the measurement, and it touched no answer log other than local's own throwaway rows.)

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

---

## Decision — closed 2026-08-14 for local and dev

**No grandfather clause. Issue 008 ships the `CLEARED_RESULTS` flip with no date
predicate and no cutover date.**

Zero `revealed` rows exist on either database, so zero questions reopen and no child is
pulled back a single level. There is nothing to grandfather. Adding a date predicate to
`loadClearedIds` would be dead code guarding a case that cannot occur — and dead code in
the cleared-set path is exactly the kind of thing that later gets mistaken for a rule.

If the prod run (below) comes back non-zero, this decision is reopened, and the clause
goes in then. The measurement is cheap to re-run and the flip has not shipped yet, so
deferring it costs nothing.

### The prod run is a gate on prod rollout, not on this ticket

Prod is the DigitalOcean managed cluster and was deliberately not queried. This ticket is
scoped to local and dev, which is where 008 will be built and tested.

**Before 008 is promoted to prod**, run the same script against prod and confirm the
count is still zero:

```bash
DATABASE_URL="<prod-url>" node scripts/quiz-revealed-blast-radius.js
```

A non-zero `revealed` count there means real children are mid-progress on questions that
are about to reopen, and the flip must not ship until the clause is added. Recorded as a
promotion gate in [000-index.md](000-index.md).

### The finding worth carrying forward

Across local and dev: **55 answer rows, zero `revealed`, zero `wrong`.** Both the API
(`ANSWER_RESULTS`) and the worker (`quizVerdictResults`) accept all three verdicts, so the
pipeline is not filtering them out.

On dev data this is explainable as staff smoke-testing — 9 answers per device, and a
tester answers their own quiz correctly. **On prod it would not be.** If prod also shows
zero `revealed` across real children, the reveal path is not firing, and requirement 4
reverses a decision that never takes effect. Check the verdict spread first when the prod
run happens; it is a bigger question than the grandfather clause.
