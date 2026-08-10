---
status: closed
assignee: claude
---

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

- [x] `riddle-bank-3-5.csv`, `riddle-bank-6-8.csv` and `riddle-bank-9plus.csv` each hold
      30 riddles: 10 per level across levels 1-3
- [x] `riddle-bank-all.csv` exists as the combined export
- [x] `accepted_answers` is pipe-separated everywhere; no commas
- [x] Every `code` is unique across all files
- [x] Import runs clean and is idempotent — a second run changes no rows
- [x] Import exits non-zero if any band/level lacks exactly 10 active riddles
- [x] Band 3-5 played end to end in a live local session, all 10 of level 1 answered
- [x] No riddle's answer_text duplicates another's within the same band

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`

## Resolution

Content shipped as `454436f2` in `manager-api-node` on `feat/riddle-bank`. All eight
criteria pass, the last one live across three sessions on 2026-08-06.

**Verified against the running dev stack**, not just by reading the files:

- 90 rows in `riddle_question`, exactly 10 active per band/level for all nine groups
- `import:riddle-bank:dry` clean on all four files (30/30/30/90 rows, 0 skipped)
- Idempotent: a snapshot of all 90 rows taken before and after a second import is
  byte-identical once `update_date` is excluded, and no row was added
- Deleting one row from `riddle-bank-3-5.csv` makes the importer print
  `error: 3-5 / en / level 2 has 9 active question(s), expected 10` and exit 1
- The API serves the new content: with the simulator device in band 3-5,
  `GET /quiz/next-questions?character=riddle_master` returns level 1 ids 31-40,
  `bank=riddle`, `answered_today=0`

**Cross-band answer reuse is deliberate.** "a table" is a 3-5 riddle and a 6-8 riddle
with different clues, and a device only ever sees the band its kid profile derives. The
no-duplicates rule is per band, which is what the ticket asked for.

**`riddle-bank-all.csv` is the only file that spells `active` out as `true`**; the
per-band files leave the column empty, which `parseQuizRow` already reads as active.
That mirrors how the quiz bank files are split and was not a choice made here.

**Five `accepted_answers` lists were widened after review**, where the tight version
would have told a child they were wrong when they were right: `a pine tree` for the
cactus riddle, `fog` for darkness, `sound` for light, common bird species for the
3-5 bird riddle, and `an auto | a rickshaw` for the school-run riddle.

### Verified live 2026-08-06, band 3-5

The last criterion needs a real microphone. `handleDataMessage` in
`pkg/livekit/room_session.go` accepts only `ready_for_greeting`, `end_prompt`,
`shutdown_request`, `abort` and `session_language_update` — there is no text-injection
topic, so a child's answer can only arrive as audio through STT, and the dashboard's Test
tab calls `getUserMedia`. Driving `POST /quiz/answer` instead would have faked the
criterion and cleared the level the real run needed, so it was not done. A human ran it.

All ten of band 3-5 level 1 answered `correct`, ids 31-40, confirmed from
`riddle_question_answer` rather than the logs. `progress` afterwards:
`current_level: 2, levels_completed: 1`, and the next batch is 10 fresh level-2 riddles.
Two riddle rows and no quiz rows were written per answer, so the banks stayed separate.

**The Daily Ten closed for the first time.** `daily_quiz.md` after the first session:

```
MEMO: type=daily_quiz | date=2026-08-06 | status=completed | answered=10 |
first_try=8 | with_hint=2 | missed=0 | scored_q=40 | ... | parent_summary=Fantastic
job! Rahul solved 10 riddles ...
```

That closes the unknown carried in from 002 — no Riddler session had ever reached ten, so
the day gate and the `status=completed` MEMO were unproven for this character.

**It took three sessions, and the reason is worth recording.** The first was staged on a
false reading: the throwaway prep script passed the MAC as `00163eacb538` while answers
are stored `00:16:3e:ac:b5:38`, so `clearDayGate` backdated zero rows and reported
`{ backdated: 0 }`, indistinguishable from "nothing to clear". `quiz_bank.md` therefore
said "4 of 10 scored so far today" — from the morning's 6-8 run — and Riddler correctly
started at the fourth riddle and stopped at ten, answering 34-40. It behaved exactly as
designed; the setup was wrong.

`resolveDeviceContext` normalizes the MAC, but `macFilter` does not, and `macFilter` is
what `loadClearedIds`, the day-gate count, `leastRecentlyPlayedLevel`, `recordAnswer` and
`clearDayGate`'s raw SQL all use. A colonless MAC therefore resolves the right age band
while seeing zero progress. Out of scope here; raised as its own task.

**Two guards fired during the third session and both held.** The model reported a verdict
for `memo_id=32` when only id 33 was pending; `Quiz MEMO id not in batch; corrected to the
only pending question` remapped it. The repeat verdict on the next turn was dropped:
`Quiz verdict already reported; dropped as duplicate`.

The kid profile's `birth_date` was moved to 2022-06-15 for the run and **restored to
2018-06-15** afterwards; the prep script and its saved value were deleted.
