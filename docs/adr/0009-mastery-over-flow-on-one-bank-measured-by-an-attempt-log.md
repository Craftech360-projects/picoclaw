# 9. Quizzy trades flow for mastery, on one shared bank, measured by an attempt log

Date: 2026-08-14

## Status

Accepted

Reverses the progression rule set by
[docs/issues/quiz-bank/006-quizzy-prompt-cutover-e2e.md](../issues/quiz-bank/006-quizzy-prompt-cutover-e2e.md).
Extends [ADR-0005](0005-quizzy-scored-questions-come-from-a-curated-bank.md), whose
"progress is derived, never stored" rule and server-owns-game-logic rule both survive
unchanged.

## Context

Quizzy asks a question, offers a hint after a wrong answer, and reveals the answer after
a second wrong answer. A revealed question then counts as cleared and the child moves on.
That is not a defect. It is a deliberate, documented choice — ticket 006, line 15: *"Two
wrong tries → reveal the answer warmly, emit `result=revealed` (nothing blocks
progression)."* The original design chose flow over mastery so a struggling child would
never be stuck.

The cost of that choice is that a child can complete every level having been told a large
share of the answers, and the record cannot tell anyone which. Three further facts,
established by reading the live prompt and the answer log rather than by inference:

**The record is thinner than the game.** The prompt classifies answers into four buckets —
FIRST_TRY, WITH_HINT, MISSED, UNCLEAR — but the `MEMO` contract carries only
`result=correct|revealed`. WITH_HINT collapses into `correct` on the way to the database.
So today's `correct` rows conflate unaided and hinted success, and **no mastery rule can
be backfilled from existing data**. `wrong` is never emitted by Quizzy at all, despite
appearing in `ANSWER_RESULTS` and in the published parent-app contract.

**Eight age-banded banks is an unfundable authoring load.** Questions are authored per
`(age_band, level)` across bands 3–5, 6–8 and 9+, in both the quiz and riddle banks. Age
is also a poor proxy for ability: two seven-year-olds differ more than the average
seven-year-old differs from the average nine-year-old.

**Nobody is watching.** There is no per-attempt record. How many tries a question took,
which questions stall a level, and how often speech recognition was the real culprit are
all unanswerable today.

## Decision

Three coupled decisions. They are recorded together because each is unsafe alone.

### 1. Mastery over flow

A revealed answer no longer clears a question. `CLEARED_RESULTS` becomes `['correct']`
for Quizzy: a question the child did not solve comes back another day.

Riddler does **not** inherit this. A riddle whose answer you already know is not a riddle,
so the shared bank service gets a per-bank `clearOnReveal` flag and Riddler keeps today's
flow behaviour. The flag exists because `banks.js` `resolveBank` gives both characters one
service; without it the flip changes a second character silently.

The mastery bar is **Door 1 or Door 2 cleared unaided**, and a child stuck on a level
advances anyway after **3 days** — enforcement without an escape hatch is a trap, not a
standard.

### 2. One bank, not eight

`age_band` retires as a *value*, not as a column: `ageBandFromBirthDate` returns a single
constant and active rows are set to `'all'`. The column, the function and the
`(age_band, language, level)` index all stay, because retiring a value is reversible and
dropping a column is a migration.

The age range is carried by scaffolding instead of by separate content — the same question
asked plainly, then with a narrowing hint, then guided. A 4-year-old and a 9-year-old meet
the same question at different doors.

### 3. Log every attempt, and ship that first

A per-attempt log lands **before** the mastery rule, not alongside it. It records each try,
its verdict, the door, and the transcribed answer.

It is deliberately **non-authoritative**: it may fail to write without failing the child's
turn, and nothing that gates progression may derive from it. Cleared sets, `days_on_level`
and the streak all continue to derive from `quiz_question_answer`, per ADR-0005. A gate
reading the attempt log would silently stop firing whenever that write failed.

## Consequences

**A mis-scored answer now costs a child a day.** That is the point of the trade and also
its main risk, and it is why the attempt log ships first: without it, the earliest signal
that mastery is hurting a child is a parent complaint, with no data on which questions
stall or how often speech recognition was at fault. Speech-recognition normalisation ships
in the same change as the flip, never after it.

**The verdict rename is an external break, so it does not happen on the wire.**
`GET /toy/api/mobile/progress/quiz` is documented for app developers and types `result` as
`correct | wrong | revealed`. New verdicts map back on response — `solo → correct`,
`helped → correct`, `missed → revealed` — and everything new is additive (`door`,
`attempts_within_question`, `mastery`). Note this mapping is the reverse of the one the
design document originally proposed; the live prompt's semantics, not the document's,
are authoritative.

**`solo` and `helped` are indistinguishable in history.** Both map to `correct`, and the
distinction lives only in the new additive field. The mastery bar therefore applies from
the cutover forward and is never retroactive.

**No grandfather clause is needed.** Local and dev hold 55 answer rows between them with
zero `revealed` and zero `wrong`, so no question reopens and no child loses a level. A
date predicate would be dead code guarding an impossible case, in the one code path where
dead code later reads as a rule. **Before promotion to production this must be re-measured
there** — a non-zero count reopens the decision. The answer log is never rewritten; any
clause is a read-side predicate.

**Zero `revealed` rows may itself be a finding.** Both the API and the worker accept the
verdict, so nothing filters it. On dev this is explainable as staff testing. If production
also shows zero across real children, the reveal path is not firing and this ADR reverses
a rule that never takes effect — settle that before shipping.

**Collapsing the bank means re-levelling both banks by hand**, and it is the one decision
here that is hard to reverse once content is merged. `kid_learning_progress` is unique on
`(kid_id, subject, topic)` where topic reads `"<band> level <n>"`, so changing the band
orphans old achievement rows: either migrate the strings or accept a dated discontinuity,
but decide it rather than discover it. The prompt hardcodes the three age bands too, and
must be rewritten with the data.

**Two constants, not one.** `CLEARED_RESULTS` exists in both `quiz.service.js` and
`mobile.service.js`. Flipping only the first leaves the parent's dashboard reporting a
question as done while the toy reopens it.

**The model's bookkeeping load does not increase.** Doors, verdicts, mastery, streaks and
the day gate are computed server-side and handed to the model as lines to say, per
ADR-0005. Nothing here asks a 31B model to track more state than it does today.
