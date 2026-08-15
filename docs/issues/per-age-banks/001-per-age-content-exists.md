---
status: closed
assignee: claude
---

# 001 — Per-age content exists in both banks

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

The expand half of an expand → migrate → contract migration, on the **dev** database.
After this slice both banks hold content for every age 3 through 10, and every device's
existing progress has been carried onto it — while behaviour stays byte-for-byte
unchanged, because nothing reads the new rows until 002.

Three things happen, in this order, for `quiz_question`/`quiz_question_answer` and then
identically for `riddle_question`/`riddle_question_answer`:

1. **Widen the vocabulary.** Replace the `age_band` CHECK with one accepting the old
   values *and* the new ones, so both coexist.
2. **Clone.** Every active band row is copied into each of its constituent ages —
   `3-5` → 3, 4, 5; `6-8` → 6, 7, 8; `9+` → 9, 10 — with `code` suffixed `-a<age>`.
   The originals stay `active = true`. Retiring them here would be a live outage:
   the bank query filters on `active`, so children would find zero questions between
   this SQL and the 002 deploy.
3. **Remap progress.** For each answer row against an old-band question, insert the
   equivalent row against the clone in that device's **current** age bank, preserving
   `result` and `answered_at`. Without this every child restarts at level 1 and
   re-hears questions they have already cleared.

The device's age comes from `ai_device.kid_id → kid_profile.birth_date`, clamped to
3–10, matching the arithmetic `ageBandFromBirthDate` will use in 002. Devices with no
kid or no birth date map to 6.

Both the clone and the remap must be re-runnable — the remap in particular gets replayed
immediately before the 002 deploy to pick up anything answered in between.

Two accepted costs, called out so they are not rediscovered as bugs:

- `answered_today` is device-wide and not band-scoped, so remapped copies of *today's*
  rows count alongside their originals for one day. Run late evening IST, or accept one
  day where the day gate closes early.
- A child keeps only the rows remapped into their current age's bank. Their history in
  the other ages' clones is empty, which is correct — they never played that content at
  that age.

## Acceptance criteria

- [x] For each bank, every (age 3–10 × level × language) combination has exactly 10
      active rows — asserted by a query, not by eye
- [x] The old `3-5`/`6-8`/`9+` rows are still `active = true` and unmodified
- [x] No answer row was deleted; `quiz_question_answer` and `riddle_question_answer`
      row counts only grew
- [x] A device that had cleared level 1 before the migration still reports
      `current_level: 2` from `GET /quiz/progress`, for both banks
- [x] A live Quizzy session and a live Riddler session behave exactly as before —
      same band, same level, same questions
- [x] Re-running the clone and remap scripts changes nothing (verified by row counts
      before and after a second run)
- [x] `SELECT max(length(code))` confirms the `-a10` suffix keeps every code inside
      `VARCHAR(50)`

## Blocked by

None — can start immediately.

## Resolution

Shipped as `prisma/migrations/20260812000000_per_age_banks_expand/migration.sql`
(widen CHECK → clone → remap, one transaction, re-runnable) plus
`scripts/verify-per-age-banks.js`, which asserts these criteria against a live
database and exits non-zero on failure.

Applied to the local Supabase DB. Both banks went 90 → 330 questions (240 clones =
8 ages × 3 levels × 10); quiz answers 11 → 22, riddle 31 → 52. A second run of the
migration changed no counts. Longest code is now 18 characters, well inside
`VARCHAR(50)`.

Progress was verified two ways. Replaying `deriveLevelState` against the new banks
gives the same current level as the old bands for the one device with history —
quiz level 2, riddle level 3. And calling `nextQuestions` on the unchanged service
still returns `band=6-8`, the same levels and the same questions, confirming this
step changed no behaviour.

One finding worth carrying forward: **10 riddle answers did not remap, correctly.**
They are `RID-3-5-*` rows belonging to a child who is now 8, and age 8's clones come
from the `6-8` band, so no `-a8` twin of a `3-5` question exists by construction.
This is the dormant-progress case in 000-design §3, met in the wild on the first
run. The verifier reports dormant rows as a count rather than failing on them; an
earlier draft of the check counted every old-band answer as mappable and failed
here, which is what surfaced it.

Not verified locally, by design: a real voice session. The old rows are untouched
and active and no application code changed, so session behaviour cannot differ —
but nobody has watched one. That happens on the dev box, on approval.

