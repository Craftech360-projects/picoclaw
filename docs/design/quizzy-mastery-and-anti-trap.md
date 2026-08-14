# Mastery & Anti-Trap

> **Status**: In Design — `/design-review` 2026-08-14: **NEEDS REVISION**, all 5
> blocking + 5 recommended + 2 nice-to-have items applied. Pending independent
> re-review (the review was run by the authoring session).
> **System**: #17 in [quizzy-systems-index.md](quizzy-systems-index.md) · Tier: Mastery · Layer: Feature
> **Parent design**: [quizzy-redesign-gdd.md](quizzy-redesign-gdd.md) §M2, §M3
> **Last Updated**: 2026-08-14
> **Implements Pillar**: P1 (Figuring out beats being told) and P5 (Never trap a child)
> **Review mode**: lean — CD-GDD-ALIGN skipped (not a PHASE-GATE)
> **Specialist input**: `systems-designer` (§Formulas) and `qa-lead` (§Acceptance Criteria)
> **not consulted — those agents are not installed.** Both sections authored directly;
> review them manually with extra care.

---

## Overview

Mastery & Anti-Trap decides, once per day per child, **whether a level is finished
and what happens if it isn't.** It replaces the current rule — where a question the
child never answered still counts as cleared — with one where a level closes only
when the child has produced the answers themselves, and it guarantees that no child
can be stuck on a level forever.

The child never interacts with this system directly. They experience it as *"we're
still on this one"* or *"you did it — new level tomorrow!"* — two sentences that
must carry the entire state of a game with no screen.

It exists because requirement 4 has no meaning without it. Repeating a level is
only valuable if the repeat is *earned*, and only safe if the repeat can *end*.
Those two rules pull in opposite directions, and this system is where the tension
is resolved.

---

## Player Fantasy

**Indirect** — this is infrastructure the child feels the effects of, never the
system itself.

The target feeling is **"I got it"** — the specific satisfaction of producing an
answer you couldn't produce yesterday. Not "I passed", not "I scored": *I know
this now, and I didn't before.*

The failure mode to design against is not frustration, it is **shame**. A child who
hears "we're still on level three" for the third day must hear it as *Quizzy is
still here with me*, never as *I am behind*. So this system's outputs are always
narrated in the first-person plural and always paired with something earned:

> ❌ "You still haven't finished level three."
> ✅ "Two more to go on this one — and you got the spider one today, that was tricky!"

The emotional anchor: a parent overhearing should be able to tell the child did
well **even on a day the level didn't pass.** If they can't, this system is
misconfigured regardless of what the numbers say.

---

## Detailed Design

### Core Rules

1. **A question is cleared only by a `solo` verdict.** `solo` means the child
   produced the answer at **Door 1 or Door 2** without being walked to it.
   `helped` (Door 3) and `missed` do not clear.
2. **A level's question set is fixed** — the ten authored rows sharing
   `(age_band, level)`. Mastery never changes which questions belong to a level.
3. **A level is PASSED when all ten of its questions are cleared.** Nothing else
   passes a level.
4. **A level that is not PASSED at day end is OPEN.** It reopens tomorrow serving
   **only its uncleared questions**, and each **reopens at Door 1** — every child
   gets a fresh unaided chance each day, which is the only route by which a
   `helped` question can become `solo`.
   > **Reworded.** The first draft said "a different Door than its last attempt"
   > and declared a `last_door_used` interface for it. The repeat-opening decision
   > (always Door 1) post-dates that and makes the old rule unsatisfiable — a child
   > who went silent at Door 1 yesterday would get Door 1 again, i.e. the same
   > Door. `last_door_used` is deleted; see [quizzy-doors.md](quizzy-doors.md)
   > §Interactions. **Consequence: the anti-memorisation burden now rests entirely
   > on model paraphrase of Door 1** — Doors Open Question 2 tracks it.
5. **The anti-trap cap: after 3 consecutive days on the same level, the level
   becomes PRACTISED and the child advances.** PRACTISED is a terminal state; the
   level is never re-opened by this system.
6. **Uncleared questions from a PRACTISED level enter the spaced-repetition pool**
   and return as **bonus questions outside the Daily Ten**, 7 days later. A bonus
   question answered `solo` clears **the question** — it leaves the pool and stops
   recycling — but **does not change the level**, which stays PRACTISED (Formula 3,
   `ever_practised`). One answered `helped` or `missed` returns again later.
   **Bonus questions never block, never re-open a level, and never count against
   the child.**
7. **Day-end is owned by the Day Gate (system #14), not by this system.** Mastery
   evaluates state; it does not decide when the day ends. A level completed on
   question six still ends the scored day — that existing behaviour is unchanged.
8. **All state is derived from the answer log.** No mastery flag, no day counter,
   no PRACTISED marker is stored. This follows ADR-0005 and is what makes adding a
   question to an old level correctly pull the child back to finish it.
9. **Per-bank behaviour is a flag, not a rule.** `banks.js` gains
   `clearOnReveal`: `false` for quiz (mastery), `true` for riddle (flow). Riddler's
   behaviour is unchanged by this system.

### States and Transitions

**Question state** — derived per `(kid_id, question_id)`:

| State | Entry condition | Exit condition | Clears? |
|---|---|---|---|
| `UNSEEN` | No attempt rows exist | First attempt recorded | — |
| `SOLO` | Verdict `solo` at Door 1 or 2 | **Terminal** | ✅ Yes |
| `HELPED` | Verdict `helped` (Door 3, walked to it) | A later `solo` on the same question | ❌ No |
| `MISSED` | Verdict `missed` (never produced) | A later `solo` on the same question | ❌ No |
| `SOLO` *(late)* | `solo` on a bonus question from the pool | **Terminal** | ✅ Yes — but see below |

> **Not a distinct state or verdict.** A bonus item answered unaided is an ordinary
> `solo`; the pool is where it came from, not what it is. Recording lateness is a
> *reporting* concern (join on `bonus_due_date`), never a state-machine one.

**Level state** — derived per `(kid_id, level)`:

| State | Entry condition | Transitions to |
|---|---|---|
| `UNSTARTED` | No attempts on any question in the level | `OPEN` |
| `OPEN` | ≥1 attempt exists, <10 cleared, days-on-level < 3 | `PASSED` or `PRACTISED` |
| `PASSED` | All 10 cleared | **Terminal** unless a question is added to the level |
| `PRACTISED` | days-on-level = 3 and <10 cleared | **Terminal** — never reopens |

**Interruptibility.** A turn cancelled mid-question writes **no verdict**. The
existing bridge only persists a MEMO on a completed final reply
([agent_bridge.go:815](../../pkg/livekit/agent_bridge.go)) — preserve that
exactly. An unjudged answer must never advance mastery, and a dropped wifi
connection must never consume a child's day.

**PRACTISED is deliberately terminal and deliberately irreversible.** A level that
could un-practise would let a child be pulled back into content they were already
released from, which is the exact trap P5 forbids.

### Interactions with Other Systems

| System | Direction | Interface |
|---|---|---|
| **#8 Progress Derivation** | reads from | Supplies cleared-question set and current level. Mastery redefines *cleared* — this is the change that ripples. |
| **#10 Attempt Log** | reads from | Source of Door used, verdict, and attempt count. **Mastery cannot function without it** — hard dependency. |
| **#13 Verdict Reporting** | reads from | Delivers `solo`/`helped`/`missed`. Hard dependency. |
| **#15 Doors** | **reads from** (one-directional) | Consumes `door` per attempt to derive `solo` (Doors 1–2) vs `helped` (Door 3). **Mastery supplies nothing back** — `last_door_used` was deleted when repeats became always-Door-1. *(Note: the circular dependency in the systems index is Doors ⇄ Attempt Log, which is unaffected and still stands.)* |
| **#14 Day Gate** | peer | Mastery reports PASSED; the Gate decides the day is over. Neither owns the other. |
| **#18 Spaced Repetition** | writes to | Emits `(kid_id, question_id, due_date)` on PRACTISED. |
| **#20 Spark Streak** | no coupling — by decision, not by accident | Streak derives from the answer log independently (parent §6a-3). It counts *days played*, not levels passed, so **a PRACTISED day keeps the streak**. That is a deliberate design choice — never punish a child twice for the same hard level — not a statement that the two systems are unrelated. |
| **#23 Analytics** | writes to | Must expose mastery **additively**. `result` stays `correct\|wrong\|revealed` on the wire (published contract). |
| **#3 Bank Registry** | reads from | `clearOnReveal` flag. |

---

## Formulas

### 1. `cleared_set`

`cleared_set(scope, level) = { q ∈ level : ∃ attempt(scope, q) with verdict = 'solo' }`

| Variable | Type | Range | Description |
|---|---|---|---|
| `scope` | `{kid_id: bigint} \| {device_mac: text}` | — | `kid_id` when present, else `device_mac`. **Not a single scalar** — the two keys have different types, so a `bigint` field cannot hold the fallback. |
| `level` | int | 1..max_authored | Level under evaluation |
| `q` | bigint | — | `question_id` |

> **`mastered_late` was not a verdict and has been removed.** The first draft tested
> `verdict = 'solo' ∨ verdict = 'mastered_late'`, but the verdict vocabulary is
> `solo | helped | missed` (Doors rule 10) and nothing emits a fourth value. A
> bonus question answered unaided emits an ordinary `solo`; being late makes it no
> different. See the `SOLO (late)` row in the question-state table.

**Output range:** 0 to `level_size` (normally 10).
**Example:** 8 solo, 1 helped, 1 missed → `|cleared_set| = 8`.

### 2. `days_on_level`

```
level_anchor(kid, level) = max(
    first date the child attempted this level,
    max(create_date) over questions added to this level after that date
)

days_on_level(kid, level) = |{ distinct date(answered_at @ 'Asia/Kolkata')
                               : rows in quiz_question_answer for this level
                                 where date ≥ level_anchor }|
```

| Variable | Type | Range | Description |
|---|---|---|---|
| `answered_at` | timestamptz | — | From **`quiz_question_answer`** — see the source note below |
| `level_anchor` | date | — | The day this level's current run began |
| timezone | const | `Asia/Kolkata` | **Must** match `promptTimeBand` ([quiz_state.go:588](../../pkg/livekit/quiz_state.go)) |

**Output range:** 1 to `ANTI_TRAP_CAP` (3) within a run. Reaching the cap
terminates the level.
**Example:** attempts on Aug 12, 13, 14 IST → `days_on_level = 3` → PRACTISED.

> **The anchor exists because a derived count cannot be "reset".** The first draft
> said adding a question to a PASSED level "resets `days_on_level` to 0". It
> cannot — the count is derived from rows that still exist, so a level with 3 days
> of history would evaluate **PRACTISED immediately** and the newly added question
> would never be served. That silently skipped new content, the exact failure
> ADR-0005's derived-progress model exists to prevent, and it made AC 6
> unsatisfiable. Anchoring the window to the newest question's `create_date` gives
> the child a fresh 3 days for new content while keeping everything derived.

> **Source is `quiz_question_answer`, NOT the attempt log.** This is a safety
> requirement, not a preference. The attempt log is an *observation* log whose
> write is explicitly allowed to fail (§Edge Cases). If `days_on_level` read from
> it, a run of failed writes would undercount and **the anti-trap cap would never
> fire — a child stuck forever.** P5 is the one guarantee that must not depend on
> a best-effort table. Everything else in this document may read the attempt log;
> this must not.

> **Timezone is load-bearing.** Casting in UTC breaks after 18:30 IST — peak usage
> — splitting one evening session across two "days" and burning the cap in a
> single sitting.

### 3. `level_state`

```
level_state(scope, level) =
    PRACTISED  if ever_practised(scope, level)                      // sticky, tested FIRST
    PASSED     if |cleared_set| = level_size
    PRACTISED  if days_on_level ≥ ANTI_TRAP_CAP ∧ |cleared_set| < level_size
    OPEN       if 0 < attempts ∧ |cleared_set| < level_size ∧ days_on_level < CAP
    UNSTARTED  otherwise

ever_practised(scope, level) =
    ∃ a day d where days_on_level(as of d) ≥ CAP ∧ |cleared_set(as of d)| < level_size
```

**Two ordering rules, and they are not the same rule:**

1. **`ever_practised` is tested first, so PRACTISED is sticky.** Once a level has
   been released it stays released, and a later bonus `solo` cannot promote it to
   PASSED. **This resolves a contradiction in the first draft**, where rule 6's
   retroactive clear would have flipped a level the state table called terminal.
   Decision: the child already had their reward beat and moved on; retroactively
   re-grading a finished level is bookkeeping they cannot perceive, and it would
   let a *terminal* state reopen — precisely what PRACTISED exists to prevent.
   A late bonus `solo` still clears the **question** (it leaves the pool and stops
   recycling); it just does not change the **level**.
2. **Within a live run, PASSED beats the cap.** A child who clears the last
   question *on* day 3 has PASSED, not PRACTISED — the cap must never steal a
   legitimate win.

### 4. `mastery_score` (reporting only, never gates anything)

`mastery_score = round(100 × (solo_door1 + 0.7 × solo_door2) / level_size)`

| Variable | Type | Range | Description |
|---|---|---|---|
| `solo_door1` | int | 0..`level_size` | Cleared unprompted |
| `solo_door2` | int | 0..`level_size` | Cleared on the two-way choice |
| weight | const | **0.7** | Door 2 discount |

**Output range:** 0–100. Ten Door-1 clears → 100. Ten Door-2 clears → 70.

**Why 0.7 and why this exists.** Door 2 is a 50/50, so a guessing child scores ~50
by chance; weighting it at 1.0 would make luck indistinguishable from knowledge in
the parent report. It **does not affect progression** — a Door 2 `solo` clears the
question outright, per the mastery bar chosen. This number is presentation only.

> **Starting value: 0.7.** Test: compare `mastery_score` against Door-1 re-test
> performance on the same questions 7+ days later. Pass if score correlates with
> retention (r > 0.4). If Door-2 clears retain as well as Door-1, raise toward
> 1.0; if they retain far worse, lower toward 0.5.

### 5. `bonus_due_date`

`bonus_due_date = practised_date + SPACED_GAP_DAYS`

| Variable | Type | Range | Description |
|---|---|---|---|
| `practised_date` | date | — | Day the level hit the cap |
| `SPACED_GAP_DAYS` | const | **7** | Days before the item returns |

**Output:** a date. On or after it, the item is eligible as **one** bonus question
per day, oldest-due first.

> **Starting value: 7 days.** Grounded in spaced-repetition practice (expanding
> intervals), but the specific number is a starting value, not a citation. Test:
> `solo` rate on returning bonus items. Pass if >50% — that indicates genuine
> consolidation rather than the gap being either too short (still unlearned) or so
> long it's effectively a fresh question. Below 35%, shorten to 4 days.

**Constants**

| Constant | Value | Notes |
|---|---|---|
| `ANTI_TRAP_CAP` | **3** days | The P5 guarantee |
| `SPACED_GAP_DAYS` | **7** days | |
| `DOOR2_WEIGHT` | **0.7** | Reporting only |
| `MAX_BONUS_PER_DAY` | **1** | Protects session length |
| `level_size` | authored count | **Not hardcoded to 10** — see edge cases |

---

## Edge Cases

- **If a question is added to a PASSED level**: the level reopens, and
  `level_anchor` moves to the new question's `create_date`, so `days_on_level`
  counts from that day — a fresh 3-day run for the new content. Existing ADR-0005
  behaviour, deliberately preserved. *(The anchor is how a derived count achieves
  what the first draft wrongly described as "resetting to 0" — see Formula 2.)*
- **If a question is added to a PRACTISED level**: the level stays PRACTISED. Do
  **not** reopen it. PRACTISED means "released"; re-trapping a child on authoring
  activity is exactly the P5 failure.
- **If a level has fewer than 10 authored active rows**: `level_size` is the
  authored count. Never compare against a hardcoded 10 — a level of 7 would
  otherwise be unpassable forever.
- **If a question is deactivated (`active = false`) mid-level**: it leaves
  `level_size`. If that makes cleared = size, the level PASSES immediately.
- **If the child clears the final question on day 3**: PASSED wins. Evaluation
  order in formula 3 guarantees this.
- **If all attempts occur in one calendar day**: `days_on_level = 1`. The cap
  cannot be exhausted in a single sitting — it counts *days*, not sessions.
- **If a session crosses local midnight**: the day is fixed at session start and
  held for the whole session. A child playing at 23:55 must not have the day
  change under them mid-level.
- **If `kid_id` is null on attempt rows**: fall back to `device_mac` scope, and
  **suppress all mastery narration** ("two more to go", the streak). An unreliable
  statement said confidently is worse than silence.
- **If two children share one device with distinct `kid_id`s**: mastery is
  per-kid, correctly. The Day Gate is per-device (known limitation, system #25) —
  so child B may find the day already spent. **Out of scope here; must not be
  silently "fixed" by making mastery device-scoped.**
- **If the attempt log write fails but the answer row succeeds**: mastery still
  derives correctly from `quiz_question_answer` — the attempt log is an
  observation log. Door history is lost, so tomorrow's Door escalation falls back
  to Door 1. Degrade, never fail.
- **If the bank is unreachable**: no scored play, no mastery evaluation, no day
  consumed. ADR-0005 unchanged.
- **If a bonus question and a Daily Ten question are both due**: the Daily Ten
  wins. Bonus is appended after, max one, and only if the day isn't complete.
- **If a PRACTISED level's bonus item is never answered `solo`**: it recycles
  indefinitely at `MAX_BONUS_PER_DAY`. Acceptable — it is one question, unscored,
  and never blocks. **But log the recycle count**: an item recycling 5+ times is a
  content defect (ambiguous question, or STT can't hear the answer), not a
  learning problem.
- **If the child answers a question correctly without being asked it** (volunteers
  the answer to a question later in the batch): no verdict. Only the asked
  question is judged. Prevents accidental clears from free chat.

---

## Dependencies

**Hard** — this system cannot function without them:

| System | Interface |
|---|---|
| #10 Attempt Log | reads `(kid_id, question_id, door, verdict, attempted_at)` |
| **#15 Doors** | reads `door` per attempt — without it `solo` and `helped` are indistinguishable |
| #13 Verdict Reporting | reads `solo\|helped\|missed` per judged question |
| #8 Progress Derivation | reads the level's authored question set |
| #1 Question Bank | reads `(age_band, level, active)` |

**Soft** — degrades gracefully:

| System | Degradation |
|---|---|
| #18 Spaced Repetition | Without the pool, PRACTISED items are simply dropped. Weaker, not broken. |

> `#15 Doors` was previously listed here as soft while the Interactions table called
> it bidirectional. It is **hard** — without `door` per attempt, `solo` and `helped`
> are indistinguishable and mastery cannot be derived at all. Moved to the table above.

**Depended on by:** #14 Day Gate, #18 Spaced Repetition, #23 Analytics.
(#20 Streak is *not* a dependent — it derives from the answer log independently.)

> **Provisional:** #10, #15, #18 have no implementation. Their interfaces above are
> this GDD's proposal, not an agreed contract. #10 must be built first — it is the
> only one that blocks. Note that `days_on_level` deliberately does **not** depend
> on #10 (Formula 2).

---

## Tuning Knobs

| Knob | Default | Safe range | Too high | Too low |
|---|---|---|---|---|
| `ANTI_TRAP_CAP` | 3 days | 2–5 | Child stalls; parent sees no progress for a week | Mastery becomes decorative — requirement 4 stops meaning anything |
| `SPACED_GAP_DAYS` | 7 | 3–14 | Effectively a fresh question; no consolidation | Still unlearned; feels like punishment |
| `MAX_BONUS_PER_DAY` | 1 | 1–3 | Session length creeps; the Daily Ten stops being ten | Pool drains too slowly to matter |
| `DOOR2_WEIGHT` | 0.7 | 0.5–1.0 | Luck reads as knowledge in the parent report | Real Door-2 clears look like failures |
| `clearOnReveal` (per bank) | quiz `false`, riddle `true` | boolean | — | — |

**Interaction warning:** `ANTI_TRAP_CAP` and `SPACED_GAP_DAYS` are coupled. Cap at
2 with a 14-day gap means items leave the child's working memory before returning
— the repetition stops being spaced repetition and becomes a cold re-test. Keep
`SPACED_GAP_DAYS ≥ 2 × ANTI_TRAP_CAP`.

---

## Visual/Audio Requirements

No visual channel exists. Audio is the entire output surface.

| Event | Requirement |
|---|---|
| Level PASSED | The full Reward Beat sting (system #21). Unmistakably the biggest sound in the game. |
| Level OPEN at day end | A **warm continuation** cue — "see you tomorrow", not a failure tone. **No negative sound anywhere in this system.** |
| Level PRACTISED | Same warmth as PASSED, slightly shorter. The child must not be able to hear that they were force-advanced. |
| Bonus item cleared | A small distinct chime. Recognition without inflating it into a level win. |

**Mandatory:** every one of this system's four outcomes has an audio signature.
With no screen, an unvoiced state transition is invisible — a child who cannot
hear the difference between PASSED and PRACTISED cannot know what happened.

📌 **Asset Spec** — run `/asset-spec system:mastery-and-anti-trap` once the audio
direction is approved.

---

## UI Requirements

None. No display. Mastery state reaches the child only through Quizzy's speech,
and through the parent app (system #23) which has its own UX surface.

The **spoken** requirement stands in for UI and is binding: at session start,
Quizzy must state the level and remaining count in one short clause
(*"we're still on the spider one and two others"*). P4 — no screen means no
unspoken state.

---

## Acceptance Criteria

**Core rules**

1. **GIVEN** a level with 10 questions and 9 `solo` + 1 `helped`, **WHEN** the day
   ends, **THEN** the level is `OPEN` and tomorrow's batch contains exactly the 1
   helped question.
2. **GIVEN** a question answered `helped` yesterday at **any** Door, **WHEN** it is
   served today, **THEN** it is asked at **Door 1**. *(Tightened from "a Door other
   than 3", which the always-Door-1 decision makes both weaker and ambiguous.)*
3. **GIVEN** 10 `solo` verdicts on a level, **WHEN** the last is recorded, **THEN**
   the level is `PASSED` and the Reward Beat fires.
4. **GIVEN** a level with attempts on 3 distinct IST dates and 8 cleared, **WHEN**
   state is evaluated, **THEN** it is `PRACTISED`, the child advances, and the 2
   uncleared questions have `bonus_due_date = today + 7`.
5. **GIVEN** a `PRACTISED` level, **WHEN** a new question is added to it, **THEN**
   it remains `PRACTISED` and the child is not pulled back.
6. **GIVEN** a `PASSED` level with 3 days of attempt history, **WHEN** a new
   question is added, **THEN** `level_anchor` moves to that question's
   `create_date`, `days_on_level` returns **1** on the next play, and the level is
   `OPEN` with only that question served — **not** `PRACTISED`.
6a. **GIVEN** a `PRACTISED` level, **WHEN** a bonus item from it is later answered
   `solo`, **THEN** the **question** is cleared and leaves the pool, and the
   **level** remains `PRACTISED` — it does not become `PASSED`.
7. **GIVEN** `clearOnReveal = true` (riddle bank), **WHEN** a riddle is revealed,
   **THEN** it clears — Riddler's behaviour is unchanged by this system.
7a. **GIVEN** a level is `PASSED` and the Daily Ten is not exhausted, **WHEN** the
   day ends, **THEN** the Day Gate closed it — mastery reported state and did not
   decide the day *(rule 7)*.
7b. **GIVEN** the schema is inspected, **THEN** no stored mastery flag, day counter,
   or PRACTISED marker exists on any table *(rule 8 — state is derived, never
   stored)*.

**Formulas**

8. **GIVEN** 6 Door-1 and 4 Door-2 clears, **WHEN** `mastery_score` is computed,
   **THEN** it returns 88 (`round(100 × (6 + 2.8) / 10)`).
9. **GIVEN** attempts at 23:55 and 00:30 IST on consecutive dates, **WHEN**
   `days_on_level` is computed, **THEN** it returns 2 — **and** the session that
   began at 23:55 evaluated its whole run as day 1.
10. **GIVEN** a level with 7 active authored rows and 7 cleared, **WHEN** state is
    evaluated, **THEN** it is `PASSED` (not blocked awaiting a non-existent 10th).
11. **GIVEN** cleared count reaches `level_size` on day 3, **WHEN** state is
    evaluated, **THEN** `PASSED` is returned, never `PRACTISED`.

**Edge / integration**

12. **GIVEN** a turn cancelled mid-question, **WHEN** the session resumes, **THEN**
    no verdict row exists and the question is served again unchanged.
13. **GIVEN** attempt rows with `kid_id = NULL`, **WHEN** the greeting is composed,
    **THEN** no mastery count and no streak is narrated.
14. **GIVEN** the attempt-log write failed but the answer row succeeded, **WHEN**
    state is evaluated, **THEN** mastery is still correct and Doors fall back to 1.
14a. **GIVEN** a level where **every** attempt-log write failed across 3 days,
    **WHEN** state is evaluated, **THEN** `days_on_level` still returns 3 and the
    level becomes `PRACTISED`. *(The P5 anti-trap guarantee must survive total loss
    of the observation log — this is why Formula 2 reads `quiz_question_answer`.)*
15. **GIVEN** the bank is unreachable, **WHEN** the session runs, **THEN** no
    mastery evaluation occurs and no day is consumed.
16. **GIVEN** a bonus item has recycled 5 times, **WHEN** it recycles again,
    **THEN** a content-defect warning is logged with the question id.
17. **GIVEN** two `kid_id`s on one device, **WHEN** each plays, **THEN** mastery is
    tracked separately per child.

**Non-functional**

18. **GIVEN** a child with 200 attempt rows, **WHEN** `next-questions` is called,
    **THEN** level state resolves inside the existing call with **no additional
    round trip** and no measurable latency regression.

---

## Open Questions

| # | Question | Owner | Blocks |
|---|---|---|---|
| 1 | How many existing `revealed` rows will reopen, and do we grandfather pre-cutover ones? | eng | Rollout, not design |
| 2 | Does the parent app need a `mastery_score` tile, or is it internal only? | product | #23 additive fields |
| 3 | Should `ANTI_TRAP_CAP` vary by age once per-age data exists? Cap is currently uniform on a shared bank. | design | Post-launch tuning |
| 4 | Day Gate is per-device, mastery is per-child (#25). What does sibling B hear when A has spent the day? | design | Ships as a known limitation |
| 5 | Is `mastery_score` worth having at all if it gates nothing? | design | Could be cut to simplify |

---

## Provisional Assumptions

1. **#10 Attempt Log exists with the proposed schema.** If `door` or `verdict`
   isn't recorded per attempt, **rules 1 and 4** are unimplementable and this GDD
   needs revision, not workarounds. *(The first draft cited rule 2 — a level's
   question set is fixed — which has no attempt-log dependency at all.)*
2. **Doors are server-assigned.** If the model chooses the Door, the `door` value
   in the attempt log is unreliable and the `solo`/`helped` split — the whole basis
   of mastery — becomes noise.
3. **`level_size` is readable per level.** Assumed derivable from
   `COUNT(*) WHERE active`.
4. **One level per day stays true.** If a child can ever play two levels in a day,
   `days_on_level` stops being a meaningful cap and the anti-trap rule needs
   re-deriving from sessions rather than days.
