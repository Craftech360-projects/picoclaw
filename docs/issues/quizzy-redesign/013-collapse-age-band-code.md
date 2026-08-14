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
- [ ] **`AGE_BANDS` in `scripts/lib/quiz-import.js` accepts `'all'`** — added by issue 007. It is currently the closed set `'3'..'10'` and rejects anything else loudly by design, so without this every re-levelled row in 014's sheet is skipped. Bands are already per-age; this collapse is 8 → 1, not 3 → 1.
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
- **Re-read the prompt on DB1 first.** 001 read it from local only. §5's age bands are
  rewritten here; confirm DB1's copy matches before editing.
  See [000-index.md](000-index.md).


---

## Findings — 2026-08-14: done, and the column is gone entirely

`manager-api-node` `24c4c788`. 599 unit tests pass; verified against local.

### Scope changed mid-ticket, deliberately

The ticket (and ADR-0009) said retire the *value*, keep the column: retiring a value is
reversible, dropping a column is a migration. That reasoning held while the value still
meant something.

It stopped meaning anything. Every active row carried the identical string, the CHECK
constraint guarded a constant, and the `(age_band, language, level)` index led with a
column that never varied. **A column that can only hold one value is not a reversibility
hedge — it is a field every future reader has to stop and ask about.** So it was dropped,
on the user's call. Reversing it is a nullable column plus a backfill, which is no harder
than the collapse would have been.

Scope is the **quiz and riddle banks only**. Nothing else in the schema had an `age_band`.

### The published contract did not move

`age_band` and `age_band_defaulted` are still served — now the constant `'all'` and a plain
"no child profile" flag. This is exactly what 005 froze the wire for: an internal change
this large reached no app developer. Verified on `next-questions` and `progress`, both banks.

`ageBandFromBirthDate` is deleted rather than left returning a constant. The index is now
`(language, level)`, which is what selection actually filters and orders on. The importer
still accepts an `age_band` column and discards it, so sheets written before this load
unchanged.

### The CHECK constraint nobody knew about

`quiz_question_age_band_check` enforced "active rows must be one of `'3'..'10'`" — the
database guarding the *previous* migration. It is not in `schema.prisma`, because Prisma
does not model CHECK constraints, so it only surfaced by rejecting the collapse. Dropped
with the column.

**Worth knowing generally: there may be other CHECK constraints this schema file does not
show.** Anything that changes a column's allowed values should expect one.

### ⚠ Level 1 is now an 80-question level

The eight former bands each held ten questions at level 1, so the collapse makes Level 1
eighty questions. The Daily Ten still caps at ten a day, so nothing breaks — but a child
now needs **eight days to clear Level 1**, and the importer's ten-per-level rule flags
every level as over-full.

This is the content half, and it is 014's whole job. **013 makes the system correct;
014 makes it playable.** Until then the ladder is technically working and practically
wrong, which is worth knowing before anyone demos it.
