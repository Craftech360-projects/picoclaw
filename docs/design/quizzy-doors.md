# The Doors — Ask Scaffolding

> **Status**: In Design — `/design-review` 2026-08-14: **NEEDS REVISION**, all 6
> blocking + 4 recommended + 2 nice-to-have items applied. Pending independent
> re-review (the review was run by the authoring session; see caveat below).
> **System**: #15 in [quizzy-systems-index.md](quizzy-systems-index.md) · Tier: Mastery · Layer: Feature
> **Absorbs system #16 (Micro-teach)** — Door 3 *is* the micro-teach; splitting them
> produced two documents describing one turn. No separate GDD exists for #16.
> **Parent design**: [quizzy-redesign-gdd.md](quizzy-redesign-gdd.md) §M1 and §M2a
> **Sibling GDD**: [quizzy-mastery-and-anti-trap.md](quizzy-mastery-and-anti-trap.md) — its rules constrain this one
> **Last Updated**: 2026-08-14
> **Implements Pillar**: P3 (Difficulty lives in the ask) and P1 (Figuring out beats being told)
> **Review mode**: lean — CD-GDD-ALIGN skipped (not a PHASE-GATE)
> **Specialist input**: `systems-designer` (§Formulas) and `qa-lead` (§Acceptance Criteria)
> **not consulted — not installed.** Both authored directly; review with extra care.

---

## Overview

The Doors decide **how** a question gets asked, not which question. One bank row
can be delivered three ways — open, two-way choice, or guided count-along — and the
server escalates through them as a child struggles. Difficulty moves out of the
content and into the delivery.

This is the system that makes requirement 1 possible. A single shared bank can
serve a 3-year-old and a 10-year-old only because the same row reaches them
differently. Without the Doors, one bank means content pitched at nobody.

The child never perceives a "Door". They perceive Quizzy noticing they're stuck
and trying a kinder way — which is the entire point.

---

## Player Fantasy

**Both** — the child experiences the Door directly (it changes what they hear) but
never as a system.

The target feeling is **being met where you are**. Not "the game got easier" —
children detect condescension fast, and a 7-year-old told they're on the easy
version stops trying. The feeling is *Quizzy is helping me get there*, which is a
different emotion entirely: collaboration, not accommodation.

This is why escalation must be **narrated as intent, never as verdict**:

> ❌ "That's wrong. Let me make it easier."
> ✅ "Ooh, tricky one — let me give you a choice."

The reference is a good teacher at a kitchen table, not adaptive difficulty in a
video game. A good teacher rephrases without announcing that you failed. The
Door change *is* the rephrasing.

**Anti-goal:** the child must never learn to stay silent to get an easier
question. If Door 2 feels like a reward for not trying, this system is broken —
see **Edge Cases → Abuse cases**.

---

## Detailed Design

### Core Rules

1. **Every question opens at Door 1.** No child is pre-judged by age, profile, or
   history. This holds on repeat days too — a returning question reopens at Door 1
   (per Mastery AC 2 and the repeat-opening decision).
2. **One attempt per Door, then escalate.** A wrong answer moves to the next Door
   immediately. The child never hears the same phrasing twice in a row.
3. **All three Doors run in one sitting.** A child who cannot answer is scaffolded
   to the answer *today*. Nobody ends a session having never reached it.
4. **Door 3 always ends with the child speaking the answer.** Quizzy does not say
   the final word. This is P1, and it is what makes the verdict `helped` rather
   than `revealed`.
5. **The server assigns the Door.** The worker receives an explicit `ask_mode` per
   question and per attempt. **The model never chooses.** ADR-0005's lesson applied
   again: decide in code, hand the model a line to say.
6. **Door 2's wrong option is authored**, stored on the bank row. The model never
   invents a distractor — an invented one can be accidentally correct.
7. **Door 2 randomises option order** per ask. See Formula 3 — this is not cosmetic.
8. **Silence is not a wrong answer, and does not consume the attempt.** A
   non-response gets **one gentle re-prompt at the same Door — one per Door, so up
   to three per question.** The re-prompt itself costs no attempt. Silence *after*
   the re-prompt consumes the attempt and escalates. A distracted 4-year-old is
   not failing; a child who has now been asked twice has had their turn.
9. **A low-confidence STT result is not an attempt.** Re-ask warmly at the same
   Door; do not consume the attempt (parent GDD §6b). This is separate from and
   additional to the silence re-prompt in rule 8.
10. **Doors report what was used.** Every attempt writes `door` to the attempt log
    so Mastery can distinguish `solo` (Doors 1–2) from `helped` (Door 3).

### Door 3 in detail — the micro-teach

Door 3 is not "the hint Door". It is **the only place in Quizzy where teaching
happens**, and today the product has no equivalent: the current flow reveals the
answer and moves on. Reveal is telling. Door 3 is teaching, and the difference is
what makes a repeated level worth repeating.

**Structure — three parts, in this order, no exceptions:**

| Part | Content | Length |
|---|---|---|
| 1 · The reason | One concrete, physical *why* — `teach_text` | 12–18 words total for parts 1+2 |
| 2 · The bridge | A partial statement the child can complete | — |
| 3 · The handback | The question, reopened, so the child speaks last | short |

> ❌ "It's eight! Well done for trying."
> ✅ "Four legs on each side, so it can walk right across a web — so how many altogether?"

**Rules:**

1. **Quizzy never says the final answer.** If the child cannot produce it, the
   verdict is `missed` and Quizzy moves on warmly. Supplying the answer to close
   the loop would recreate `revealed`, the exact behaviour this redesign removes.
2. **12–18 words for the explanation.** A voice explanation longer than that is
   not heard by a 6-year-old — it is waited through. Length is a comprehension
   constraint, not a style preference.
3. **The *why* must be concrete and physical.** "Four on each side" works.
   "Arachnids are characterised by eight legs" does not. Reason about things a
   child can picture or count.
4. **`teach_text` is authored, never generated.** It lives on the bank row. A ~31B
   model improvising explanations to young children is precisely the hallucination
   surface ADR-0005 closed — and a wrong explanation is worse than no explanation,
   because the child will remember it.
5. **The handback is mandatory.** Every Door 3 ends with the question reopened.
   The child having the last word is what converts being told into working it out.
6. **One re-prompt on silence, then `missed`.** No second teach — repeating the
   same explanation louder is the classic bad-teacher failure.

**Verdict mapping:** child produces the answer → `helped` (does **not** clear the
question, per Mastery rule 1). Child never produces it → `missed`.

> **Starting value: 12–18 words.** Test: read 10 `teach_text` values aloud to 5-
> and 8-year-olds, then ask them to say the reason back. Pass if ≥70% recall it
> unprompted. **If they can't, cut to 10 words — do not add more.** Failure to
> recall means too much, never too little.

**Authoring cost, stated plainly:** `teach_text` is required on **every row in
both banks**, alongside `distractor`. That is the largest non-code cost in the
redesign, and Door 3 is worthless without it — a Door 3 with no `teach_text` is
just a hint, which is what the product already does badly.

### States and Transitions

Per question, within one sitting:

| State | Entry | Exit | Verdict on exit |
|---|---|---|---|
| `DOOR_1` | Question served | Correct → clear · Wrong → `DOOR_2` · Silence → `REPROMPT_1` | `solo` if correct |
| `REPROMPT_1` | Silence at Door 1 | Answer → resolve as Door 1 · Silence again → `DOOR_2` | as Door 1 |
| `DOOR_2` | Wrong at Door 1, or silence through `REPROMPT_1` | Correct → clear · Wrong → `DOOR_3` · Silence or both-options-echoed → `REPROMPT_2` | `solo` if correct |
| `REPROMPT_2` | Silence, or both options echoed, at Door 2 | Answer → resolve as Door 2 · Silence again → `DOOR_3` | as Door 2 |
| `DOOR_3` | Wrong at Door 2, or silence through `REPROMPT_2` | Child says answer → `helped` · Silence → `REPROMPT_3` | `helped` |
| `REPROMPT_3` | Silence at Door 3 | Answer → `helped` · Silence again → `missed` | `helped` / `missed` |
| `ABANDONED` | Session ends mid-question | — | **none written** |

**Re-prompt invariant:** exactly **one** re-prompt is available per Door — three
per question maximum. A re-prompt never consumes an attempt (rule 8), so
`attempt_no` and the Door advance together and cannot desynchronise.

**Interruptibility.** Escalation state is **per-turn and non-persistent**. If the
session drops at `DOOR_2`, tomorrow's question reopens at `DOOR_1` — there is no
resumable mid-question state. This is deliberate: partial Door state would be one
more thing to persist, and restarting is the kinder behaviour anyway.

**Resource cost.** All Doors for one question consume **one** of the ten daily
slots. Escalation is free in slot terms and costs only turns.

### Interactions with Other Systems

| System | Direction | Interface |
|---|---|---|
| **#17 Mastery** | **writes to** (Mastery is a dependent) | Emits `door` per attempt. Mastery maps Doors 1–2 → `solo`, Door 3 → `helped`. **One-directional** — see the note below on `last_door_used`. |
| **#10 Attempt Log** | writes to | `(question_id, attempt_no, door, said_raw, verdict, stt_confidence)`. Hard dependency. |
| **#7 Question Selection** | reads from | The day's question set and order. Doors never reorder or reselect. |
| **#9 Prompt Composition** | writes to | Injects the per-question `ask_mode` line into the turn's context. |
| ~~#16 Micro-teach~~ | **absorbed** | Not a separate system. Specified above as *Door 3 in detail*. |
| **#4 STT Normalisation** | reads from | Confidence gates rules 9 and the early-Door-2 jump. |
| **#1 Question Bank** | reads from | Needs `distractor` and `teach_text` columns — **both new**. |
| **#13 Verdict Reporting** | writes to | MEMO must carry the Door. Its anti-invention guards must survive Door 3's looser phrasing. |

**The circular dependency, resolved.** Doors needs the attempt count to pick a
Door; the log records which Door was used. Contract: **the server computes
`attempt_no` and assigns `ask_mode` before the turn, from rows already written;
the log records what was assigned after the turn resolves.** Read before, write
after, never negotiate within.

**`last_door_used` is deleted — it was dead on arrival.** The Mastery GDD declared
a `last_door_used` interface so a returning question could be "asked at a different
Door than its last attempt". But the repeat-opening decision — **always reopen at
Door 1** — was made *after* that GDD was written, and it makes the interface both
unread and unsatisfiable: a child who went silent at Door 1 yesterday would get
Door 1 again, i.e. the *same* Door. There is now no Doors → Mastery edge, and
Mastery's rule 4 has been reworded to match. **The anti-memorisation burden falls
entirely on model paraphrase of Door 1** — see Edge Cases and Open Question 2.

---

## Formulas

### 1. `door_for_attempt`

`door_for_attempt(attempt_no, stt_flag) = min(3, max(attempt_no, 1 + stt_flag))`

| Variable | Symbol | Type | Range | Description |
|---|---|---|---|---|
| `attempt_no` | *a* | int | 1..3 | Scored attempts on this question, this sitting. Re-prompts never increment it (rule 8). |
| `stt_flag` | *s* | int | 0 or 1 | 1 when ≥2 low-confidence reads have occurred (§6b Layer 3) |

**Output range:** 1 to 3.

| `attempt_no` | `stt_flag` | Door | Why |
|---|---|---|---|
| 1 | 0 | 1 | Clean first ask |
| 1 | 1 | **2** | Misheard → jump to the binary-choice format |
| 2 | 0 | 2 | Normal escalation |
| 2 | 1 | 2 | Already at the STT-tolerant Door; do not skip past it |
| 3 | either | 3 | Guided |

**`stt_flag` floors the Door at 2 — it never adds.** This was wrong in the first
draft: `attempt_no + stt_flag` sent a misheard child on attempt 2 straight to
**Door 3**, i.e. *past* the only ask format built to survive bad audio. Door 2 is
the STT circuit-breaker (parent GDD §6b Layer 3) precisely because a two-way
choice needs only to distinguish two known tokens, where Door 1 and Door 3 both
require recognising open speech. Flooring at 2 pulls a misheard child *to* that
Door and holds them there; adding pushed them through it.

### 2. `exchanges_for_question`

Each Door reached costs one ask, plus one more if its re-prompt fired:

`exchanges_for_question = Σ over doors reached d ∈ {1..D} of (1 + reprompt_used(d))`

where `D = door_for_attempt` at resolution and `reprompt_used(d) ∈ {0, 1}`.

| Variable | Type | Range | Description |
|---|---|---|---|
| `D` | int | 1..3 | Deepest Door reached for this question |
| `reprompt_used(d)` | int | 0 or 1 | Whether Door *d*'s single re-prompt fired |

**Output range:** **1** (clean Door 1 answer) to **6** (all three Doors, each
re-prompted).

**Example:** wrong at Door 1, silence then answer at Door 2 →
`(1 + 0) + (1 + 1)` = **3 exchanges**.

**Session turn budget.** Worst case is 10 × 6 = **60 exchanges**; realistic is
12–18. Turn count is the cost driver on a voice product
(`docs/cheeko-costing-sheet.xlsx`).

> **Corrected from the first draft**, which claimed a 4-exchange worst case by
> counting `MAX_REPROMPTS = 1` per *question*. Three separate edge cases each grant
> a re-prompt at a different Door, so the real bound is one per Door — 6 per
> question, 60 per session. The old figure understated the ceiling by 50%.

> **Starting value: cap at 6 per question, 60 per session.** Test: measure the
> exchange distribution over 50 real sessions. Pass if the p90 session is under 25
> exchanges. If p90 exceeds 35, drop to max 2 Doors per day before touching
> anything else — session length is a child-experience problem before it is a cost
> problem.

### 3. `door2_answer_position`

`position = 1 + (hash(question_id ‖ seed_key ‖ date) mod 2)`
`seed_key  = kid_id, or device_mac when kid_id is null`

| Variable | Type | Range | Description |
|---|---|---|---|
| `position` | int | 1 or 2 | Where the correct answer is spoken |
| `seed_key` | string | — | **`kid_id` is nullable** on pre-profile rows (Mastery §Edge Cases). A null in the hash input is undefined behaviour, so fall back to `device_mac`, which is never null. |
| `hash` | — | — | Any stable hash; `crc32` matches `quizPlanForDay`'s existing use |

**Output:** 1 (correct answer first) or 2 (second).

**This is not cosmetic — it prevents the most likely exploit in the system.** If
the correct answer is always spoken second ("six legs, or *eight*?"), a child
learns within days to always repeat the last thing they heard, and Door 2 stops
measuring anything. Seeding on `question_id ‖ kid_id ‖ date` keeps it deterministic
(so a re-ask inside one session doesn't flip and confuse the child) while varying
across questions, children, and days.

**Even so, treat Door 2 as a 50/50.** It clears the question, but Mastery weights
it at 0.7 for reporting precisely because chance cannot be designed out of a
two-way choice — only made unlearnable.

### 4. `ask_mode` payload

Per question in the `next-questions` response:

```jsonc
{
  "id": "1234",
  "ask_mode": "open",        // open | choice | guided
  "attempt_no": 1,
  "question_text": "How many legs does a spider have?",
  "answer_text": "eight",
  "choice_order": ["eight","six"], // Door 2 only — resolved server-side by Formula 3
  "teach_text": "four on each side, so they can walk on webs"  // Door 3 only
}
```

**`distractor` is deliberately absent from the payload.** The first draft sent both
`distractor` and `choice_order`, which is the same fact twice with no stated
precedence — if they ever disagreed, nothing said which wins. `choice_order` is
already the ordered, resolved form, so it is the only one the worker needs.
`distractor` stays a **bank column**; it just never crosses the wire.

**Constants**

| Constant | Value | Notes |
|---|---|---|
| `MAX_DOORS` | 3 | |
| `ATTEMPTS_PER_DOOR` | 1 | Re-prompts do not count as attempts (rule 8) |
| `MAX_REPROMPTS_PER_DOOR` | 1 | **Per Door**, not per question — so 3 per question max |
| `STT_EARLY_JUMP_THRESHOLD` | 2 | Low-confidence reads before `stt_flag` = 1 |
| `MAX_EXCHANGES_QUESTION` | 6 | 3 Doors × (ask + re-prompt) |
| `MAX_EXCHANGES_SESSION` | 60 | Safety valve |

---

## Edge Cases

- **If the child answers correctly at Door 1 before the question finishes**:
  accept it. `solo`. Children interrupt; that is engagement, not error.
- **If the child answers with the *other* Door 2 option**: `wrong`, escalate to
  Door 3. No partial credit — a two-way choice has no near-miss.
- **If the child repeats both Door 2 options back** ("six or eight?"): treat as
  silence, not a wrong answer. This enters `REPROMPT_2` — asking them to pick one.
  Consumes Door 2's re-prompt, **not** the attempt.
- **If `distractor` is missing on a bank row**: **skip Door 2 entirely**, go Door
  1 → Door 3. Never generate a distractor at runtime (rule 6). Log the gap as a
  content defect with the question id.
- **If `teach_text` is missing**: Door 3 degrades to a hint-and-ask without the
  explanation, and the verdict is still `helped`. Log the gap. **Do not let the
  model improvise the teach** — that is the ADR-0005 boundary.
- **If the distractor is accidentally also correct** (authored badly — "is a
  tomato a fruit, or a vegetable?"): unfixable at runtime. Mitigation is authoring
  review; detection is a high `wrong` rate at Door 2 on a specific question. **Add
  this to the content audit's checks.**
- **If STT confidence is low on every read**: `stt_flag` fires and the Door is
  **floored at 2** — the binary-choice format, which is the one that survives bad
  audio. It does **not** jump to Door 3. Only normal escalation reaches Door 3. If
  even Door 2 cannot be parsed, verdict `missed`, and flag the question as
  STT-suspect so it isn't mistaken for a learning failure.
- **If the child goes silent at Door 3**: `REPROMPT_3` fires once, then `missed`.
  Never supply the answer to close the loop — that would make it `revealed`, the
  exact behaviour this redesign removes.
- **If the session ends mid-escalation**: no verdict, no slot consumed, question
  reopens at Door 1 tomorrow.
- **If the same question returns for a 3rd day**: still opens at Door 1. The child
  hears identical Door 1 phrasing three days running.
  **Mitigation — and this is a real gap in the "always Door 1" choice:**
  Quizzy paraphrases Door 1 in her own words each time (already the existing
  instruction), so the wording varies even though the Door doesn't. **But this
  puts the entire anti-memorisation defence on model paraphrase**, which is weak.
  Watch for children reciting answers without comprehension; if seen, revisit the
  repeat-opening decision.
- **If `MAX_EXCHANGES_SESSION` is hit mid-level**: end the day gracefully at the
  current question. Do not truncate mid-question — resolve or abandon cleanly.
- **If the question is a bonus (spaced-repetition) item**: full Door ladder
  applies. It never blocks, so a `missed` bonus simply recycles.

### Abuse cases

- **Gaming silence to reach an easier Door.** Staying quiet does not skip ahead:
  silence costs a *turn* (the re-prompt) without advancing the Door, so reaching
  Door 2 by silence takes **two** exchanges where answering wrong takes **one**.
  Silence is strictly the slower route, so there is nothing to exploit — and Door
  2 still requires recognising the right answer. **Detection signal: watch the
  silence rate at Door 1.** A rising rate means either the exploit is being found
  anyway or Door 1 is pitched too hard.
- **Always answering with the last option heard at Door 2.** Neutralised by
  Formula 3 randomising position. Detection: a child whose Door 2 accuracy tracks
  answer position rather than question difficulty.
- **Answering wrong deliberately to reach Door 3's teach.** Real, and *not worth
  preventing* — a child who wants the explanation is a child who wants to learn.
  It costs them `helped` instead of `solo`, so the level still won't pass, and the
  incentive corrects itself.

---

## Dependencies

**Hard:**

| System | Interface |
|---|---|
| #1 Question Bank | **needs new `distractor` and `teach_text` columns** |
| #10 Attempt Log | writes `door`, `attempt_no`, `stt_confidence` |
| #7 Question Selection | reads the day's ordered question set |
| #9 Prompt Composition | injects `ask_mode` per turn |

> **#17 Mastery is a *dependent*, not a dependency** — it consumes `door` to derive
> `solo` vs `helped`. The first draft listed it here, inverting the direction. The
> Interactions table above says "writes to"; this table now agrees with it.

**Soft:**

| System | Degradation |
|---|---|
| #4 STT Normalisation | Without confidence, `stt_flag` is always 0 — Doors still escalate on wrong answers, just without the audio circuit-breaker |

**Depended on by:** #17 Mastery, #13 Verdict Reporting.

> **Provisional:** #10 is unbuilt; #1's two new columns don't exist.
> The `ask_mode` payload above is this GDD's proposal, not an agreed contract.
> **#10 blocks — build it first.**

---

## Tuning Knobs

| Knob | Default | Safe range | Too high | Too low |
|---|---|---|---|---|
| `MAX_DOORS` | 3 | 2–3 | n/a | At 2, no child is ever taught — Door 3 *is* the teaching |
| `ATTEMPTS_PER_DOOR` | 1 | 1–2 | Repeating identical phrasing reads as Quizzy not listening | n/a |
| `MAX_REPROMPTS_PER_DOOR` | 1 | 0–1 | At 2, worst case is 9 exchanges per question — 90 a session | At 0, a moment's distraction is scored as failure |
| `STT_EARLY_JUMP_THRESHOLD` | 2 | 1–3 | Misheard children stay at Door 1 being marked wrong | At 1, one bad read floors every child at Door 2 |
| `MAX_EXCHANGES_SESSION` | 60 | 40–80 | Cost and attention both blow out | Days end unfinished for slower children |

**Interaction warning:** `ATTEMPTS_PER_DOOR` and `MAX_REPROMPTS_PER_DOOR`
**multiply** through all three Doors — the worst case is
`MAX_DOORS × (ATTEMPTS_PER_DOOR + MAX_REPROMPTS_PER_DOOR)`. At the defaults that
is 3 × 2 = 6. Raising **either** to 2 gives 9; raising both gives 12, i.e. **120
exchanges a session.** Raise one or the other, never both, and re-derive
`MAX_EXCHANGES_SESSION` when you do.

---

## Visual/Audio Requirements

Audio is the only channel, and the Door transition is the most important sound in
this system.

| Event | Requirement |
|---|---|
| **Door 1 → 2 escalation** | A warm "let's try together" cue. **Never a buzzer, never a descending tone.** The child must not hear "you failed" — they must hear "we're changing approach." This single sound carries the Player Fantasy. |
| **Door 2 → 3** | Same family, slightly softer. Signals help arriving, not defeat. |
| **Silence re-prompt** | Gentlest cue in the game, or none at all — just Quizzy's voice. |
| **Solo at Door 1** | The biggest per-question reaction. Unaided recall earns the strongest response. |
| **Solo at Door 2** | Real, warm, and audibly smaller than Door 1. |
| **Child says it at Door 3** | Genuinely celebratory — they got there. This must not sound like a consolation prize. |

**Mandatory and non-negotiable: no negative sound anywhere in this system.** A
voice-only product cannot soften a harsh tone with friendly visuals. The
escalation cue is heard by a struggling child at their least confident moment.

📌 **Asset Spec** — run `/asset-spec system:doors` once audio direction is approved.

---

## UI Requirements

None — no display. The Door is expressed entirely in phrasing and the escalation
cue. Parent-facing Door distribution belongs to system #23.

---

## Acceptance Criteria

**Core rules**

1. **GIVEN** a fresh question, **WHEN** it is served, **THEN** `ask_mode = "open"`
   and `attempt_no = 1`, regardless of the child's age or history.
2. **GIVEN** a question answered `helped` at Door 3 yesterday, **WHEN** it is
   served today, **THEN** `ask_mode = "open"` (Door 1).
3. **GIVEN** a wrong answer at Door 1, **WHEN** the next turn is composed, **THEN**
   `ask_mode = "choice"` and both options are present.
4. **GIVEN** a wrong answer at Door 2, **WHEN** the next turn is composed, **THEN**
   `ask_mode = "guided"` and `teach_text` is included.
5. **GIVEN** Door 3 completes with the child speaking the answer, **THEN** verdict
   is `helped` — **never** `revealed`.

**Door 3 / micro-teach** (absorbed from #16)

5a. **GIVEN** any Door 3 delivery, **WHEN** the turn is spoken, **THEN** it
    contains a reason, a bridge, and a reopened question — and **Quizzy has not
    spoken the answer.**
5b. **GIVEN** the child cannot produce the answer at Door 3, **WHEN** the re-prompt
    also fails, **THEN** verdict is `missed` and Quizzy moves on warmly **without
    supplying the answer.**
5c. **GIVEN** a `teach_text` value, **WHEN** parts 1+2 are counted, **THEN** the
    total is 12–18 words.
5d. **GIVEN** silence at Door 3, **WHEN** the re-prompt is composed, **THEN** it is
    a fresh bridge, **not** a repeat of the same explanation.
6. **GIVEN** all three Doors resolve within one session, **THEN** exactly **one**
   of the ten daily slots was consumed.
7. **GIVEN** any turn, **WHEN** the worker composes context, **THEN** `ask_mode`
   came from the server response and **no model output influenced it.**

**Formulas**

8. **GIVEN** `attempt_no = 1` and `stt_flag = 1`, **WHEN** `door_for_attempt` runs,
   **THEN** it returns **2** — floored at the STT-tolerant Door, never skipped past.
8a. **GIVEN** `attempt_no = 2` and `stt_flag = 1`, **THEN** it returns **2**, not 3.
9. **GIVEN** `attempt_no = 5` (impossible but defensive), **THEN** it returns 3,
   never 4 or higher.
10. **GIVEN** the same `(question_id, seed_key, date)`, **WHEN** Door 2 is asked
    twice in one session, **THEN** `choice_order` is **identical** both times.
10a. **GIVEN** `kid_id` is null, **WHEN** Formula 3 runs, **THEN** `seed_key` is
    `device_mac` and a valid position is returned — no null enters the hash.
11. **GIVEN** **1000** Door 2 asks across varied questions and children, **WHEN**
    positions are tallied, **THEN** the correct answer is first in **45–55%** of
    cases. *(Widened sample from 100 — a fair coin misses a 40–60 band on 100 draws
    about 4% of the time, which would fail a correct implementation.)*
12. **GIVEN** all three Doors reached with each re-prompt fired, **THEN**
    `exchanges_for_question` = **6** and `MAX_EXCHANGES_QUESTION` is not exceeded.
12a. **GIVEN** a wrong answer at Door 1 and a silence-then-answer at Door 2,
    **THEN** `exchanges_for_question` = 3.

**Edge / integration**

13. **GIVEN** a bank row with no `distractor`, **WHEN** Door 1 fails, **THEN**
    Door 2 is skipped, Door 3 is served, and a content-defect warning logs the id.
14. **GIVEN** a bank row with no `teach_text`, **WHEN** Door 3 is served, **THEN**
    it is a hint-and-ask, verdict is still `helped`, and the gap is logged.
    **The model must not generate a teach.**
15. **GIVEN** silence at Door 1, **WHEN** the child then answers correctly at the
    re-prompt, **THEN** verdict is `solo`, `attempt_no` is **still 1**, and no
    escalation occurred.
15a. **GIVEN** silence at Door 1 **and** silence again at `REPROMPT_1`, **THEN**
    `attempt_no` becomes 2 and the Door escalates to 2.
15b. **GIVEN** a question where all three Doors each fire their re-prompt, **THEN**
    exactly 3 re-prompts occurred and none incremented `attempt_no` on its own.
16. **GIVEN** two consecutive low-confidence reads, **WHEN** the next Door is
    assigned, **THEN** `stt_flag = 1`, neither read consumed an attempt, and the
    Door is **≥ 2 and ≤ 2** unless `attempt_no` is already 3.
17. **GIVEN** the session drops at Door 2, **WHEN** the child returns tomorrow,
    **THEN** the question opens at Door 1 and no verdict was written.
18. **GIVEN** the child answers correctly before Quizzy finishes the question,
    **THEN** it is accepted as `solo`.
19. **GIVEN 20 recorded Door 3 transcripts**, **WHEN** `questionTextMatchesBank`
    runs ([quiz_state.go:135](../../pkg/livekit/quiz_state.go)), **THEN** **≥19
    verdicts survive** and are not dropped as invented.
    *(Rewritten — the first draft ended "test before trusting it", which made the
    pass condition unknowable and duplicated Open Question 1. The risk is unchanged
    and now lives there: Door 3's guided phrasing may share no content words with
    the bank question, and that guard exists because four invented questions once
    reached the database. **Do not loosen the guard to make this pass** — if it
    fails, the fix is a Door-aware branch, not a lower threshold.)*

**Non-functional**

20. **GIVEN** 50 real sessions, **WHEN** exchange counts are measured, **THEN** p90
    is under 25 exchanges per session.

---

## Open Questions

| # | Question | Owner | Blocks |
|---|---|---|---|
| 1 | Does `questionTextMatchesBank` survive Door 3? If not, the guard needs a Door-aware relaxation — carefully, since it prevents invented questions being scored. | eng | **Ships before Doors** |
| 2 | Is model paraphrase enough anti-memorisation defence given Door 1 restarts daily? | design | Post-launch observation |
| 3 | Should Door 2's distractor vary across days for the same question (multiple authored distractors)? Stronger, but another authoring multiple. | design | Content planning |
| 4 | Is 45 exchanges/session the right cost ceiling? Needs a real per-turn cost figure. | product | Before rollout |
| 5 | Should older children skip Door 3's teach if they got it at Door 2? Currently no distinction. | design | Nice-to-have |

---

## Provisional Assumptions

1. **`distractor` and `teach_text` can be added to `quiz_question`.** Both are
   new authoring obligations on **every row, in both banks** — the largest
   non-code cost in the redesign. The importer needs both columns before any
   content work starts.
2. ~~**The worker can receive and honour a per-question `ask_mode`.**~~
   **RESOLVED 2026-08-14 — the assumption holds, and the surprise did not
   materialise.** The batch *is* injected once per session: `RenderQuizQuestions`
   substitutes `{{QUIZ_QUESTIONS}}` into the greeting inside `GenerateGreeting`,
   and there is no refresh path on that placeholder.

   But the per-turn path is elsewhere. `buildMessages` runs every turn and
   already injects dynamic system directives (the voice rules, the RFID language
   lock), so the Door line follows that same pattern — **no change to
   `RenderQuizQuestions` was needed**.

   One constraint shapes it: the Door line is anchored at the **tail**, after the
   conversation, *not* at the after-first-system anchor the language lock uses.
   The prompt cache breakpoint sits on the static system block and OpenAI-side
   caching is prefix-based, so a directive that changes every turn inserted up
   there would invalidate the cached prefix on every single turn. The language
   lock is safe there only because it is fixed for the session.

   Escalation within a sitting is the worker counting tries against the ladder
   the server authored — the whole ladder ships at fetch, so no HTTP round trip
   lands inside a voice turn. See ticket 009 for that deviation from the
   read-before/write-after contract above.
3. **STT confidence is available to the worker.** If the provider doesn't expose
   it, `stt_flag` is permanently 0 and the audio circuit-breaker is lost.
4. **One attempt per Door is legible to a child.** Untested. The existing prompt
   uses two tries at one phrasing; this changes the rhythm children already know.
