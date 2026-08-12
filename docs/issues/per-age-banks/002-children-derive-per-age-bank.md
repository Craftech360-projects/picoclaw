---
status: open
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

- [ ] `ageBandFromBirthDate` unit table: age 2 → `'3'`, 3 → `'3'`, 5 → `'5'`, 6 → `'6'`,
      9 → `'9'`, 10 → `'10'`, 11 → `'10'`, missing birth date → `null`
- [ ] `GET /quiz/next-questions` for a device whose kid is 4 returns `age_band: "4"` and
      only questions from that bank
- [ ] A device with no kid profile still gets a batch, with `age_band: "6"` and
      `age_band_defaulted: true`
- [ ] Existing Jest suites pass with fixtures updated to the new vocabulary; no
      assertion is deleted to make a test pass
- [ ] One live Quizzy session and one live Riddler session, each verified from the DB:
      answer rows land against clone ids in the correct per-age bank, and the injected
      prompt block reads `band <N>`
- [ ] A device whose progress was remapped in 001 resumes at its correct level rather
      than restarting at level 1
- [ ] `CONTEXT.md` **Age Band** entry updated

## Blocked by

- `docs/issues/per-age-banks/001-per-age-content-exists.md` — deploying this first would
  point every fetch at a band value with no rows, sending every child to the free-chat
  fallback
