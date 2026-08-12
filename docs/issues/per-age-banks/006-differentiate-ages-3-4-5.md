---
status: open
assignee: rahul
---

# 006 — Differentiate ages 3, 4 and 5

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

**HITL — content authoring, human-approved.** The migration ships eight working banks by
cloning, which means age 3 and age 5 currently ask *identical* questions. This slice
makes the first three real.

Author per-age content for ages 3, 4 and 5, both banks: 3 ages × 3 levels × 10 questions
× 2 banks = 180 rows. These three ages come first because nine of ten profiled production
devices sit in the old `3-5` band — differentiating them is most of the user-visible
value of the whole redesign.

Delivered as CSVs in the same shape the importer already reads, upserting over the
existing `-a<age>` codes so there is no id churn and no progress loss: a child mid-level
keeps their cleared rows and simply meets better questions in the ones still to come.
That does mean an in-flight child can see a question change under them; acceptable, and
the reason to import outside peak hours.

Riddle answers are looser than quiz answers, so `accepted_answers` carries more weight
there. Pipe-separated; commas are rejected by the importer.

Difficulty should actually differ across the three ages — a 3-year-old's level 1 and a
5-year-old's level 1 asking equivalent questions means this slice did not happen.

## Acceptance criteria

- [ ] Ages 3, 4 and 5 each have 3 levels × 10 active questions in both banks
- [ ] No two ages share a question text within a bank
- [ ] Import is idempotent and reports zero skipped rows
- [ ] The upsert changed no question ids — a device mid-level keeps its cleared rows and
      its current level
- [ ] A human has read every row and approved it for the age it targets
- [ ] One live session per age, per bank, spot-checked for age-appropriateness against
      the real transcript

## Blocked by

- `docs/issues/per-age-banks/003-importer-per-age-vocabulary.md`
