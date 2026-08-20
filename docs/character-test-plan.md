# Character test plan — device `68:EE:8F:60:BA:AC`

One character at a time, on the local stack. Written 2026-08-20, after Wave 1–3
and ADR-0010.

After every session run:

```bash
node scripts/character-check.js 68:EE:8F:60:BA:AC
```

(from `manager-api-node/`) — it prints current state, the parent-app feed, the
no-repeat ledger and all three scored banks. Every "verify" below is a line in
that output unless it says otherwise.

---

## 0. Before the first test

**Both sides changed. The current DB state proves the old worker is still
running** — a Nani session recorded progress an hour ago but wrote nothing to
`kid_content_seen`, which only the new binary reports.

1. Rebuild the worker (`make build-livekit`) and restart it.
2. Restart local manager-api — it has new routes, new Prisma models, and the
   ADR-0010 selection changes.
3. Confirm the reset took: run the check script, play any character, run it
   again. A content character must now add a `CONTENT ALREADY GIVEN` row.

**Two facts about this device, both fine but worth knowing:**

- It is **UNLINKED** (`kid_id` null), so progress is device-scoped. Link it to a
  kid profile if you also want to prove progress follows the child.
- `math` is already **10/10 for today** and `story` holds state from an earlier
  session. Ginti will refuse a second scored run today until the day gate is
  cleared (below), and Nani will resume the saved story rather than start a new
  one — that is correct behaviour, not a bug, but it is not a clean first test.

**Resets, when you need them** — admin dashboard → Test device tab → *Reset day*
(re-opens today's Daily Ten by backdating answers; keeps cleared levels), or
*Set level* to force a level. For content characters, clearing the ledger is SQL:

```sql
DELETE FROM kid_content_seen WHERE device_mac ILIKE '68:EE:8F:60:BA:AC';
```

---

## 1. Order, and why

Plumbing first, then each new mechanism in isolation, then the scored banks
whose behaviour just changed. Stop and report at the first failure rather than
running the whole list — most of these share machinery, so one break explains
several.

| # | Character | Proves |
|---|---|---|
| 1 | **Cheeko** | persona loads, MEMO persists — nothing else moving |
| 2 | **Chanda** | second persona-only character, ritual intact |
| 3 | **Masti** | content bank serves; **repeat session** proves no-repeat |
| 4 | **Tara** | content + a cliffhanger paid off **next** session |
| 5 | **Mitthu** | word content |
| 6 | **Tikku** | MEMO ladder survives a gap |
| 7 | **Nani** | story served, **resume** mid-story |
| 8 | **Quizzy** | threshold advance, no reruns |
| 9 | **Bujho** | ungated riddles, no repeats |
| 10 | **Ginti** | math bank end to end (already partly proven) |

---

## 2. Every character — the baseline four

Check these on **all ten** before the character-specific list:

1. **Greeting is in persona** — the right name, warm, under ~40 words, straight
   into its loop. No menu, no "are you ready".
2. **Nothing leaks aloud** — no `{{PLACEHOLDER}}`, no `MEMO:` line spoken, no
   tool syntax, no markdown read out.
3. **A state row appears** after the session closes, with the right
   `state_type` and the character's name.
4. **`parent_summary` is present** in the sessions feed (Nani/Mitthu aside,
   whose MEMOs carry it only on the final turn).

If 3 fails for every character, the worker did not restart. If it fails for one,
that character's MEMO is malformed — send me the transcript.

---

## 3. Per character

### 1. Cheeko — persona only
Talk for three or four turns about anything. Ask it to remember something.
- **Verify:** `companion` state row; `parent_summary` mentions the topic.
- **Second session:** it should recall what you asked it to remember.

### 2. Chanda — persona only
The calm/bedtime ritual.
- **Verify:** state row appears; tone is the ritual, not general chat.

### 3. Masti — jokes *(the no-repeat test)*
Let it tell **all** its jokes for the session, then end cleanly.
- **Verify:** `CONTENT ALREADY GIVEN → joke N item(s)`, N ≥ 1.
- **Now run a SECOND session.** Note the jokes.
  - **Pass:** entirely different jokes; the joke count grows.
  - **Fail:** any repeat → send me both transcripts.
- This is the single most valuable test in the list: it exercises the ledger,
  the serve-time exclusion and the close-time reporting together.

### 4. Tara — wonders + cliffhanger
Let her answer a wonder and **plant a cliffhanger** before you end.
- **Verify:** `daily_wonder` state row with `cliffhanger=...` and
  `cliffhanger_paid=no`; `why` row in the ledger.
- **Second session:** she must **open by paying off** that exact cliffhanger.
  If she plants a fresh one instead, the restore is not reaching her.

### 5. Mitthu — words
A few words, meanings, an example sentence.
- **Verify:** `daily_words` state row; `word` ledger row.

### 6. Tikku — spelling ladder *(the durability test)*
Spell **three words correctly in a row** to trigger a level-up ceremony.
- **Verify:** `spell_bee` state row shows `current_level` **and** `streak`.
- **Second session:** he must start at the **earned** level, not reset to the
  age default. This is the case that was broken before Wave 3 — the ladder used
  to live only in a file pruned after 48h.

### 7. Nani — story resume
**Clear the old story state first** so this starts clean:
```sql
DELETE FROM kid_character_state WHERE device_mac ILIKE '68:EE:8F:60:BA:AC' AND state_type='story';
DELETE FROM kid_content_seen  WHERE device_mac ILIKE '68:EE:8F:60:BA:AC' AND bank='story';
```
Let her tell **two or three beats**, answer THE CHOICE, then **hang up mid-story**.
- **Verify:** `story` state row with `beat=N_of_6`, `completed=false`.
- **Second session:** she must recap in one sentence and **continue from the same
  story and beat** — not restart, not switch stories. (The story-pin fix is
  exactly this; before it she resumed beat 3 of a story she had never begun.)
- **Third session, after finishing:** a **different** story, and the finished one
  never returns.

### 8. Quizzy — threshold + no reruns
The quiz bank is untouched today, so this starts clean.
Answer **8 of 10 correctly**, deliberately miss the last two.
- **Verify:** `quiz` shows `answered 10`, `today 10/10`.
- **Second session (after *Reset day*):** with 8 cleared, the threshold should
  put you on **Level 2**, the two missed ones ride along as **bonus**, and
  **nothing you already answered is asked again**.
- **Fail signals:** a question you answered yesterday comes back as scored; or
  you are still on Level 1 with 8 cleared.

### 9. Bujho — ungated riddles
Answer a few, let one be revealed.
- **Verify:** `riddle` answers recorded.
- **Second session:** **no riddle repeats** — including the revealed one
  (Riddler keeps flow: a riddle you were told is finished).

### 10. Ginti — math
Needs *Reset day* first (already 10/10 today).
- **Verify:** problems come from the bank (`GN-` codes), verdicts logged,
  `math` count grows.
- Ginti shares Quizzy's machinery, so if 8 passed this is mostly confirmation.

---

## 4. What to send me on a failure

The transcript, plus the check-script output, plus the worker log around the
failing turn. For a stuck session, the last 20 log lines matter most — the line
before the silence is usually the answer, as it was for the PTT cancel.

---

## 5. Known-open, do not chase

- **A cancelled turn is silent by design.** If the toy sends `ptt_event
  release state=stop`, the audio is discarded deliberately. A session that
  captured genuinely nothing now says "say that one more time?" instead of going
  quiet — but the underlying question of *why the device cancels instead of
  ending the turn* (`speech_end`) is upstream in firmware/gateway and unresolved.
- Two identical Ginti session rows are recorded 2h apart in the feed with the
  same summary — possibly one session posting twice. Harmless for testing; worth
  a look if it recurs on the new binary.
