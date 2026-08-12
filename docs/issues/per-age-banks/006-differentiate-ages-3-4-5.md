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

- [x] Ages 3, 4 and 5 each have 3 levels × 10 active questions in both banks
- [x] No two ages share a question text within a bank
- [x] Import is idempotent and reports zero skipped rows
- [x] The upsert changed no question ids — a device mid-level keeps its cleared rows and
      its current level
- [ ] **A human has read every row and approved it for the age it targets** — the open
      criterion; this is why the ticket is not closed
- [ ] One live session per age, per bank, spot-checked for age-appropriateness against
      the real transcript

## Blocked by

- `docs/issues/per-age-banks/003-importer-per-age-vocabulary.md`

## Progress — drafted and imported, awaiting approval

180 rows written and imported locally as
`prisma/seed-data/{quiz,riddle}-bank-ages-3-5.csv`. **Left open on purpose**: the
criterion that defines this ticket is a human reading every row, and nothing below
substitutes for that.

The ladder, which is the whole point — the same slot at each age:

| | age 3 | age 4 | age 5 |
|---|---|---|---|
| Quiz L1 Q1 | What colour is a banana? | How many legs does a chair usually have? | What is two plus three? |
| Riddle L1 Q1 | I am yellow and long and monkeys love to eat me. | I have no legs at all but I can swim all day. | I have cities but no houses and rivers but no water. |

Quiz: age 3 is single-concept recognition (colour, animal sound, body part); age 4 adds
counting to ten, shapes, opposites and categories; age 5 reaches simple arithmetic,
letters, time, measurement and India basics. Riddles: age 3 gives one literal clue, age
4 two clues with a small inference, age 5 three clues with mild misdirection.

`accepted_answers` are generous throughout, because a five-year-old's answer reaches
the judge through STT: plurals, articles, and the Hindi words a child here would
actually say (`nani`, `hathi`, `pyaaz`, `tota`, `chhata`) are all accepted.

Verification: both files dry-ran with 0 skipped and no level-count complaints, then
imported 90 rows each. All 180 question ids were captured either side of the import and
compared — **unchanged**, so no child's cleared rows moved. The 001/005 verifier still
passes every check on both banks. No question text repeats within a bank.

Three of my own drafts were replaced before import for being near-duplicates of another
age's clue rather than genuinely different: an ant riddle at age 4 that echoed age 3's,
a second map riddle inside age 5, and a "lighter than a feather" breath riddle at age 5
almost identical to age 4's.

`{quiz,riddle}-bank-all.csv` were regenerated to carry the authored text. Left stale
they would have been a trap — re-importing them would silently revert ages 3–5 to the
clones.

### What a reviewer should look for

- Age 5 riddles lean on English wordplay: `RID-3-5-L03-Q04-a5` ("an odd number, take away
  one letter") only works if the child knows the *spelling* of seven, and
  `RID-3-5-L03-Q03-a5` (a coat of paint) turns on a pun. Both may be a poor fit for a
  child whose English is a second language.
- `RID-3-5-L03-Q07-a5` answers "a coffin" — a classic riddle, and arguably not something
  a five-year-old's toy should raise. I would cut it; flagging rather than deciding.
- Ages 9 and 10 are untouched and still serve clones of the old `9+` content.
