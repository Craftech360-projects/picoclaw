# 013 — Collapse `age_band` to `'all'` (code side)

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 2, §13 Q1.

## What to build

Requirement 1: eight age-banded banks become one ordered ladder, with M1's Doors
carrying the age range instead of separate content per band. This issue is the **code
half**; the content re-levelling is 014.

- `quiz.logic.js` — `ageBandFromBirthDate` returns a single constant. **Keep the
  function and the column.** Deleting them is a migration you don't need; retiring a
  *value* is cheaper and reversible than dropping a *column*.
- `quiz_question.age_band` — set every active row to `'all'`; keep the
  `(age_band, language, level)` index as-is.

**The trap that must not be discovered in production:** `kid_learning_progress` is
unique on `(kid_id, subject, topic)` where topic is the string `"<band> level <n>"`.
Change the band value and `"6-8 level 2"` becomes `"all level 2"` — every old
achievement row is orphaned, and a child's history shows a gap they did not earn.

Decide explicitly and record the choice: **migrate the topic strings**, or **accept the
discontinuity and note the cutover date**. Either is defensible; discovering it later is
not.

`banks[].age_band` is in the published parent-app contract (005) — it must keep
serialising, whatever value it now carries.

## Acceptance criteria

- [ ] `ageBandFromBirthDate` returns a single constant; function and column retained
- [ ] Active `quiz_question` rows set to `age_band = 'all'`; `(age_band, language, level)` index unchanged
- [ ] `kid_learning_progress` topic-string decision made, implemented, and recorded in the ADR or this issue
- [ ] If migrating strings: no child loses a previously-earned achievement row — verified by before/after counts per kid
- [ ] If accepting discontinuity: cutover date recorded, and the gap is explainable to a parent
- [ ] Parent-app endpoint still serialises `banks[].age_band` per the frozen contract from 005
- [ ] Riddler's `riddle_question.age_band` handling explicitly decided — changed or deliberately left alone
- [ ] Question selection returns the same ladder for a 3-year-old and a 10-year-old
- [ ] **Prompt §5 rewritten** — added by issue 001. The live `quiz_master` prompt hardcodes 4-5 / 6-7 / 8-10 bands with per-band speaking instructions, and §2 reads the band from `USER.md`. Collapsing the data without rewriting §5 leaves the model branching on bands the bank no longer has. Backup-and-diff before the `UPDATE`.

## Blocked by

- 003 — ADR-0009
- 007 — `teach_text` in the importer
