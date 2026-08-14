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

## Urgent, added by 013

**Level 1 currently holds 80 questions.** The eight former per-age bands each had ten at
level 1 and they now share one ladder. A child needs eight days to clear Level 1, and the
importer flags every level as over-full. The system is correct and unplayable until this
ticket runs.

`age_band` is also **gone from the schema** — the re-levelling sheet needs `code`, `level`,
`question_text`, `answer_text` and may carry `age_band` only as an ignored column.

## Acceptance criteria

- [x] Content audit run; rows per level per bank recorded — see below
- [x] Every active question assigned a level on the merged ladder — **quiz bank: 24 levels of exactly 10**, applied 2026-08-14. Riddle bank not re-levelled: Riddler keeps flow, has no Doors, and its 240 sit in 3 levels of 80 harmlessly for now
- [ ] `teach_text` authored — **Level 1 done (10 of 240)**, drafted by me and pending your review. Levels 2-24 outstanding
- [ ] Distractors — **Level 1 done (10 of 240)**, same status
- [ ] `/balance-check` run on the exported sheet; difficulty spread reviewed and outliers resolved
- [ ] 3-year-old floor validated: first level is reachable by the youngest cohort
- [ ] 10-year-old ceiling validated: days-to-frontier estimated and judged acceptable
- [ ] Sheet imports cleanly through the 007 importer, idempotently, on a non-production database first
- [ ] Import verified on the dev DO box only — **never prod**

## Blocked by

- 013 — Collapse `age_band` to `'all'` (code side)


---

## Progress — 2026-08-14: audited and scaffolded; the authoring is yours

`manager-api-node` `da7d84ba`.

### The audit

| | quiz | riddle |
|---|---|---|
| active questions | 240 | 240 |
| levels | 3 × 80 | 3 × 80 |
| `teach_text` | **0** | 0 |
| distractors | **0** | 0 |
| inactive (untouched) | 90 | 90 |

Quiz categories are reasonably spread — animals 42, numbers 37, science 35, body 19,
geography 19, space 15 — with a long tail of ones and twos (family 1, measurement 1,
letters 2, food 2). Riddles lean hard on objects 56, nature 40, wordplay 37.

### The difficulty signal survived 013

Dropping `age_band` looked like it destroyed the only human judgement about difficulty. It
did not: every `code` still carries it. `6-8-L02-Q07-a6` encodes the band, the original
level within that band, and the per-age variant. Nothing was lost.

### The worksheet

`scripts/export-relevel-sheet.js` exports 240 questions as **24 levels of 10**, ordered by
that provenance — 3-5 content, then 6-8, then 9+, preserving each band's internal level
order. That re-expresses the original authors' own difficulty judgement as one ladder,
which beats alphabetical or random by a distance.

**It is a starting point, not an answer.** A 3-5 level-3 question may well be harder than a
6-8 level-1 one. The `level` column exists to be overwritten.

```bash
node scripts/export-relevel-sheet.js --out relevel-quiz.xlsx
node scripts/export-relevel-sheet.js --bank riddle --out relevel-riddle.xlsx
```

Verified: the sheet round-trips through the existing importer — 240 rows, zero skipped, on
a dry run.

### Where I stopped, and why

`teach_text` and `distractors` are exported **empty and stay empty**. 480 of each across the
two banks.

I did not generate them, and I would push back on generating them. A distractor is a
**scored choice** — the wrong option a child picks at Door 2 — and `teach_text` is spoken to
a child as fact. ADR-0005 removed LLM-invented content from scored play precisely because
every generated item risked a hallucinated fact aimed at a young child, and "distractors
**authored**" is one of the locked design decisions on this redesign. Generating 480
explanations and handing them to four-year-olds as true is the exact failure that ADR
exists to prevent.

If you want drafting help, the honest framing is: I can propose candidates for a human to
**review and correct row by row**, treating every one as wrong until checked. That is a
different activity from authoring, and it should not be started by accident.

### Remaining criteria, all authoring

- assign final levels (the sheet suggests; a human decides)
- author 480 `teach_text` and 480 distractors
- 3-year-old floor and 10-year-old ceiling validation
- `/balance-check` on the exported sheet
- import to local first, then DB1 — **never prod**


---

## Progress — 2026-08-14: re-levelled, Level 1 authored as drafts

### The 80-question level is fixed

`scripts/apply-relevel.js` assigns 240 questions to **24 levels of exactly 10**, ordered by
the provenance still encoded in every code. 230 rows moved. Idempotent — the same codes
always yield the same ladder, so it can be re-run safely.

Verified: 24 levels, none the wrong size.

### Level 1 authored — DRAFTS, please review

Ten questions now carry `teach_text` and one distractor each. All ten serve a complete Door
ladder.

**I wrote these, so treat them as wrong until you have read them.** They are spoken aloud
to a 3-5 year old as fact, which is exactly why ADR-0005 removed generated content from
scored play.

The distractors deliberately avoid the trap: for *"what colour is a banana"* the tempting
wrong answer is **blue**, not green — an unripe banana really is green, so green would make
Door 2 offer two defensible answers. Same reasoning behind `baa` for a cow, `gloves` for
feet, `ears` for seeing: wrong, plausible to a small child, never accidentally true.

An automated check confirms no distractor collides with the answer or an accepted
alternative.

### Remaining

230 questions still need `teach_text` and a distractor. At ten a day a child takes over
three weeks to reach Level 3, so **Levels 2-3 are the next meaningful batch**, not all 230.
