# 003 — Full riddle content

## Parent

`docs/issues/riddle-bank/000-design.md`

## What to build

The remaining 80 riddles, bringing the bank to 3 bands × 3 levels × 10 = 90.

**Author band 3-5 first.** Nine of ten profiled production devices are band 3-5, and it is
the band that has never been played end to end — every hour of quiz testing so far happened
on 6-8. Order: `3-5` all three levels, then `9+`, then the two remaining `6-8` levels.

Content, not code. The importer and schema already exist from 001.

Riddle answers are looser than quiz answers, so `accepted_answers` carries more weight here
than it does in the quiz bank. A riddle whose answer is "a candle" should accept "candle",
and one answered by a child in Hindi or Tamil should be judged on meaning — the model does
the judging, so anything the prompt cannot infer belongs in `accepted_answers`.

Difficulty should climb across levels within a band, and a level-3 riddle for 3-5 should
still be easier than a level-1 riddle for 9+.

Note the ceiling this leaves: three levels is roughly three days of play, after which
`replay=true` re-serves the least-recently-played level indefinitely. That is the same
limit Quizzy has and is not addressed here.

## Acceptance criteria

- [ ] `riddle-bank-3-5.csv`, `riddle-bank-6-8.csv` and `riddle-bank-9plus.csv` each hold
      30 riddles: 10 per level across levels 1-3
- [ ] `riddle-bank-all.csv` exists as the combined export
- [ ] `accepted_answers` is pipe-separated everywhere; no commas
- [ ] Every `code` is unique across all files
- [ ] Import runs clean and is idempotent — a second run changes no rows
- [ ] Import exits non-zero if any band/level lacks exactly 10 active riddles
- [ ] Band 3-5 played end to end in a live local session, all 10 of level 1 answered
- [ ] No riddle's answer_text duplicates another's within the same band

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`
