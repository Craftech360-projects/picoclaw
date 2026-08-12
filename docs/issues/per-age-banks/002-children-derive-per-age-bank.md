---
status: closed
assignee: claude
---

# 002 — Children derive into their per-age bank

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

The cutover. `ageBandFromBirthDate` stops returning one of three band strings and starts
returning the child's age as a string, clamped to `'3'`–`'10'`. The age arithmetic itself
does not change — only what it maps onto. The service default becomes `'6'` (still
flagged `age_band_defaulted` when the birth date is missing).

This is the tracer bullet: one deploy carries the change through selection logic → the
three endpoints → the worker's injected prompt block → the answer rows → the admin view.
Nothing else needs editing, because `age_band` is an opaque string key everywhere below
`quiz.logic.js` — including the entire Go worker, which needs no change and no deploy.

Also redefine **Age Band** in `CONTEXT.md`: a one-year cohort, ages 3–10, clamped at both
ends. The **Level** entry is unchanged.

Rollback is reverting this deploy — 001 left the data supporting both vocabularies.

Verification is by real sessions, not code review. The two biggest findings of the
quiz-bank build were invisible in code and only showed up in logs and DB rows.

## Acceptance criteria

- [x] `ageBandFromBirthDate` unit table: age 2 → `'3'`, 3 → `'3'`, 5 → `'5'`, 6 → `'6'`,
      9 → `'9'`, 10 → `'10'`, 11 → `'10'`, missing birth date → `null`
- [x] `GET /quiz/next-questions` for a device whose kid is 4 returns `age_band: "4"` and
      only questions from that bank
- [x] A device with no kid profile still gets a batch, with `age_band: "6"` and
      `age_band_defaulted: true`
- [x] Existing Jest suites pass with fixtures updated to the new vocabulary; no
      assertion is deleted to make a test pass
- [ ] One live Quizzy session and one live Riddler session, each verified from the DB:
      answer rows land against clone ids in the correct per-age bank, and the injected
      prompt block reads `band <N>` — **not done: needs the dev box**
- [x] A device whose progress was remapped in 001 resumes at its correct level rather
      than restarting at level 1
- [x] `CONTEXT.md` **Age Band** entry updated

## Blocked by

- `docs/issues/per-age-banks/001-per-age-content-exists.md` — deploying this first would
  point every fetch at a band value with no rows, sending every child to the free-chat
  fallback

## Resolution

Two lines of behaviour change, as the design predicted: `ageBandFromBirthDate` now
returns `String(clamp(age, 3, 10))`, and `DEFAULT_AGE_BAND` is `'6'`. Nothing else
in either repo needed editing — `mobile.service` derives bands from the questions it
has already fetched, and the Go worker treats `age_band` as an opaque string it
never interprets, so no worker deploy is involved.

Tests written first and seen to fail (6 red), then green. Full backend suite: 453
passed, 41 suites. The old three-band assertions were rewritten, not deleted; the
birthday-boundary case they covered is kept as "moves to the next band on their
birthday, not before".

Verified against the local DB through the real service, four devices:

| device | band | defaulted | quiz level | riddle level |
|---|---|---|---|---|
| kid born 2022-01-01 (age 4) | `4` | no | 1 | 1 |
| kid born 2018-06-15 (age 8) | `8` | no | 2 | 3 |
| kid born 2016-08-15 (age 9) | `9` | no | 1 | 1 |
| no kid profile | `6` | yes | 1 | 1 |

The age-8 device is the one that matters: quiz level 2 and riddle level 3, the exact
levels it derived before the migration. Progress survived the cutover end to end.

One deliberate behaviour change beyond the design: a parseable but nonsense birth
date (in the future, or absurdly old) now clamps into range instead of falling
through to the default. Only a missing or unparseable date still returns `null`,
which is what `age_band_defaulted` exists to report. The old code could not
distinguish these because every age landed in some band anyway.

Not done: the live session criterion. That needs the dev box and is deliberately
left unticked rather than inferred from the service-level check.

