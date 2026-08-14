# 014 — Re-level both banks onto one ladder (content)

**Type:** HITL · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 2, §13 Q1.

## What to build

**The content merge is the real work of requirement 1, not the code.** Eight banks into
one ordered ladder means re-levelling every question by difficulty, and it is
**both** banks — quiz and riddle — because `riddle_question` has its own `age_band`
column too. Double the content work.

This is the one decision in the redesign that is **hard to reverse once the content is
merged** (§13 Q1). The code half (013) is reversible; this is not.

Produce a re-levelling sheet the importer (007) can load, carrying for each question:
its new level, `teach_text` for Door 3, and authored distractors. Distractors are
**authored, not generated** — a locked design decision.

Run `/content-audit` first, to see what actually exists per level before deciding the
new ladder. Today zero rows have `teach_text` or distractors, so this is authoring, not
just re-sorting.

Two things the ladder must satisfy, drawn from the playtest plan (§12):

- **The 3-year-old floor** — the youngest cohort must be able to reach a Reward Beat on
  day one. If the first level is too hard, requirement 1 needs an easier on-ramp and
  this ladder is wrong.
- **The 10-year-old ceiling** — the ladder must extend far enough that an older child
  does not hit the authored frontier in a few days.

Run `/balance-check` on the exported sheet to check the difficulty spread before import.

**Split note:** if the volume makes one ticket unmanageable, split per level rather than
per bank — a level is the unit a child experiences, and a half-re-levelled level is not
shippable.

## Acceptance criteria

- [ ] `/content-audit` run first; rows per level per bank recorded before any re-levelling
- [ ] Every active question assigned a level on the single merged ladder, both banks
- [ ] `teach_text` authored for every question reachable at Door 3
- [ ] Distractors authored (not generated) per the locked design decision
- [ ] `/balance-check` run on the exported sheet; difficulty spread reviewed and outliers resolved
- [ ] 3-year-old floor validated: first level is reachable by the youngest cohort
- [ ] 10-year-old ceiling validated: days-to-frontier estimated and judged acceptable
- [ ] Sheet imports cleanly through the 007 importer, idempotently, on a non-production database first
- [ ] Import verified on the dev DO box only — **never prod**

## Blocked by

- 013 — Collapse `age_band` to `'all'` (code side)
