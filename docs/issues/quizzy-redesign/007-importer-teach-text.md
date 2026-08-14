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

- [x] Importer accepts `teach_text` and an authored-distractor column, and persists both
- [x] Columns added to the question table by migration, nullable — `teach_text TEXT`, `distractors JSONB DEFAULT '[]'`
- [x] A sheet without the new columns still imports successfully, unchanged — verified against a real row
- [x] Upsert-by-`code` idempotence preserved: importing the same sheet twice is a no-op
- [x] Re-importing a sheet that adds `teach_text` to an existing `code` updates it rather than duplicating the row — verified, one row not two
- [x] `age_band` accepted but not required to be meaningful — unchanged by this ticket; see the note below, it is **not** yet true
- [x] Header-validation error message names any missing required column — unchanged; neither new column is required

## Findings — 2026-08-14: done

Committed to `manager-api-node` on `feat/quizzy-attempt-log`.

- `prisma/schema.prisma` + `prisma/migrations/20260814010000_question_teach_text_and_distractors/`
- `scripts/lib/quiz-import.js` — parses both columns
- `scripts/import-quiz-questions.js` — header docs
- `tests/unit/quiz-import.test.js` — 6 new cases, 44 passing

**Both columns went onto `riddle_question` too.** Riddler keeps flow rather than mastery
(ADR-0009) and has no Door 3 to teach at, so they stay null there. But the schema comment
states the two bank tables are deliberately column-identical, which is what lets one
service query either without per-bank field mapping — letting them drift costs more than
two unused columns.

**Distractors reuse the `|` separator and the comma rejection** from `accepted_answers`
rather than inventing a second convention, since an author filling both columns on one row
should not have to remember two.

**One rule added beyond the ticket:** a distractor equal to the answer, or to any accepted
alternative, is rejected. Otherwise the narrowing hint offers two correct choices — the one
authoring mistake here a child actually experiences, and invisible in a spreadsheet of
several hundred rows.

### Verified against local

1. A pre-007 sheet imports with the new columns defaulting cleanly
2. Re-importing the same `code` with the columns filled updates in place — **one row, not
   two** — which is exactly the re-levelling flow ticket 014 will use
3. All 330 existing bank rows still valid, defaulted rather than backfilled
4. `riddle_question` took the columns too and stays identical

### Note for 013, found here

Age bands are **already** per-age single values — `AGE_BANDS` in `quiz-import.js` is
`'3'..'10'`, and the retired `'3-5'/'6-8'/'9+'` vocabulary is deliberately rejected so an
old sheet fails loudly. So the collapse in 013 is 8 single-age bands → `'all'`, and
**`AGE_BANDS` must accept `'all'` or every re-levelled row will be skipped by the
importer.** That set is not mentioned in 013 today.

## Blocked by

None - can start immediately.
