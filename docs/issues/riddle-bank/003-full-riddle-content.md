---
status: open
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
- [ ] Band 3-5 played end to end in a live local session, all 10 of level 1 answered
- [x] No riddle's answer_text duplicates another's within the same band

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`

## Progress

Content shipped as `454436f2` in `manager-api-node` on `feat/riddle-bank`. Seven of the
eight criteria pass. The ticket stays open on the eighth, which needs a human at a
microphone — see below.

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

### What is left, and why an agent cannot do it

The last criterion needs a **live voice session**, and the only child-input path in the
whole stack is a real microphone: `handleDataMessage` in `pkg/livekit/room_session.go`
accepts `ready_for_greeting`, `end_prompt`, `shutdown_request`, `abort` and
`session_language_update` — there is no text-injection topic, so answers can only arrive
as audio through STT. The admin dashboard's Test tab calls `getUserMedia`. An agent has
no voice.

Driving the ten answers through `POST /quiz/answer` instead would have been faking the
criterion, not verifying it — and worse, it would have cleared 3-5 level 1 and closed the
day gate, making the real session impossible. So it was deliberately not done.

**The run is staged and ready.** Device `00:16:3E:AC:B5:38`, band 3-5, day gate open:

1. Delete `C:\Users\rahul\.picoclaw\workspace-device-00163eacb538\memory\state\daily_quiz.md`
   — it still holds `answered=4 | awaiting=5` from the 6-8 run on 2026-08-06. The bank did
   not change (riddle to riddle), only the band, so `WriteQuizBankState` does not clear it
   and the model would resume mid-scoreboard.
2. Open `http://localhost:4000`, Test tab, character **Riddler**, allow the microphone.
3. Answer all ten. Expect them in id order 31-40, starting "I am yellow and long and
   monkeys love to eat me."
4. Confirm from the rows, not the logs: 10 rows in `riddle_question_answer` for that MAC
   today, and `daily_quiz.md` showing `status=completed | answered=10`.

The kid profile's `birth_date` was moved from 2018-06-15 to 2022-06-15 to put the device
in band 3-5. **Restore it after the run** with `node riddle-3-5-testprep.tmp.js restore
00163eacb538` (untracked, in the `manager-api-node` root, alongside the saved value).

This run also closes the older unknown carried in from 002: no Riddler session has ever
reached ten, so the day gate closing at 10 and the `status=completed` MEMO are unproven
for this character in either band.
