# 007 — `teach_text` in the importer, before any re-levelling

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 1a, §14 action 4b.

## What to build

`scripts/import-quiz-questions.js` currently requires headers
`code, age_band, level, question_text, answer_text` and upserts by `code` (idempotent —
keep that). Two additions are needed before content work can start:

- **`teach_text`** — the one-breath explanation M2a needs at Door 3. Today zero rows
  have it.
- **Authored distractors** — the user's locked decision is that distractors are
  authored, not generated. The importer needs a column for them.

`age_band` becomes a constant once issue 013 lands, so the importer should accept the
column without requiring a meaningful value.

**Order matters: this ships before the content merge, not after.** Issue 014 produces a
re-levelling sheet carrying `teach_text` and distractors. If the importer can't read
those columns, the sheet won't load and the content work has to be redone.

Existing sheets without the new columns must keep importing unchanged — the migration
is additive at the file format level too.

## Acceptance criteria

- [ ] Importer accepts `teach_text` and an authored-distractor column, and persists both
- [ ] Columns added to the question table by migration, nullable
- [ ] A sheet without the new columns still imports successfully, unchanged
- [ ] Upsert-by-`code` idempotence preserved: importing the same sheet twice is a no-op
- [ ] Re-importing a sheet that adds `teach_text` to an existing `code` updates it rather than duplicating the row
- [ ] `age_band` accepted but not required to be meaningful
- [ ] Header-validation error message names any missing required column

## Blocked by

None - can start immediately.
