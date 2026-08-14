# Quizzy Redesign — Game Design Document

**Status:** Draft for review
**Date:** 2026-08-14
**Supersedes design intent of:** [ADR-0005](../adr/0005-quizzy-scored-questions-come-from-a-curated-bank.md) (bank stays; per-age banding and level-clear rules change)
**Platform constraints:** [.claude/docs/technical-preferences.md](../../.claude/docs/technical-preferences.md)

---

## 0. The One-Paragraph Version

Quizzy stops being a quiz and becomes **a thing you figure out with a friend**.
One shared question bank for every child. Difficulty stops living in the question
and moves into *how Quizzy asks it* — three escalating "Doors" that scaffold any
question down to any age. A level is ten questions; you get one level a day; it
stays open until you can answer it **yourself**, not until Quizzy tells you the
answer. Clearing a level buys you the thing children actually want, which is not
a score: it is the right to ask Quizzy a question back.

---

## 1. Player Goal & Context

**Who:** A child aged 3–10, alone with a speaking toy, usually 5–15 minutes,
often the same time each day, frequently with a parent within earshot.

**What the child is trying to do:** Not "learn". Children do not open a toy to
learn. They want the *feeling of being clever in front of someone who notices*.

**What the parent is trying to do:** See evidence their child is learning
something and coming back voluntarily.

**Design consequence:** These are two different customers with two different
reward channels. The child's reward must be immediate and social. The parent's
reward must be legible and delivered elsewhere (app/report), never mid-play.

> **The governing principle**, from Yu-kai Chou's survey of the top ten
> children's learning games: *"A learning game succeeds when a child plays it
> without knowing it's educational. The moment a child realizes it's 'learning'
> instead of 'playing', motivation collapses."* The strongest performers
> (Minecraft Edu, DragonBox, Prodigy) all put the learning act **inside** the
> fun act rather than wrapping one in the other.

**Where current Quizzy sits against that:** it wraps. It is transparently a test
with a friendly voice. It has exactly **one verb** (answer) and **one reward**
(advance). Octalysis-wise it runs almost purely on CD2 (Accomplishment) — the
weakest long-term driver, and the one that dies the moment a child stops caring
about the number.

---

## 2. Design Pillars

Every mechanic below traces to one of these. Anything that traces to none gets cut.

| # | Pillar | Means in practice |
|---|--------|-------------------|
| **P1** | **Figuring out beats being told** | The answer is never gifted. It is walked to. |
| **P2** | **The child is a person, not a score** | Quizzy remembers what *this* child likes and reuses it. |
| **P3** | **Difficulty lives in the ask** | One bank, infinite ages, via scaffolding. |
| **P4** | **No screen means no unspoken state** | If it isn't said aloud, it doesn't exist. |
| **P5** | **Never trap a child** | Every loop has an exit that preserves dignity. |

---

## 3. Core Loop

```
DAY START
   └─ Quizzy greets, names the streak, names the level
      └─ QUESTION (×10, one per turn)
            ├─ Door 1: open question          ─┐
            ├─ Door 2: two-way choice          ├─ escalate only on failure
            ├─ Door 3: guided clue → child says it  ─┘
            └─ verdict: SOLO / HELPED / MISSED
      └─ LEVEL END
            ├─ all 10 SOLO      → PASSED   → Reward Beat + Wonder Question
            ├─ some HELPED/MISSED → OPEN   → Reward Beat + Wonder Question
            └─ 3rd day on level → PRACTISED → forced advance (anti-trap)
DAY END (hard stop — one level per day)
```

The critical structural point: **the day always ends with a reward**, whether or
not the level passed. Failure costs progress, never dignity. A child who ends
five sessions in a row on "you didn't get it" stops opening the toy.

---

## 4. Mechanics

### M1 — The Three Doors (solves requirement 1)

**This is the load-bearing mechanic.** One bank serves ages 3–10 because the
*question* is fixed but the *ask* adapts. The bank row is unchanged; the voice
layer chooses a Door.

| Door | Form | Example (bank row: "How many legs does a spider have?" → 8) |
|------|------|--------------------------------------------------------------|
| **1 — Open** | Ask it straight | "How many legs does a spider have?" |
| **2 — Choice** | Two options, one right | "Does a spider have six legs, or eight legs?" |
| **3 — Guided** | Clue + count together, child says the answer | "Let's count! Four on this side… four on that side… four and four is…?" |

Rules:
- Every child starts a question at **Door 1**. Nobody is pre-judged by age.
- Escalate one Door per failed attempt, within the same turn-pair. Never skip.
- **Door 3 must end with the child speaking the answer.** Quizzy never says the
  final word. This is P1 in one sentence.
- A child who answers at Door 1 gets a *bigger* reaction than one who answers at
  Door 3 — but both get a real one.

*Why this beats per-age banks:* age is a terrible proxy for ability, birth dates
are frequently missing or wrong (the code already has an `ageBandDefaulted`
flag for exactly this), and eight banks means eight authoring obligations. One
bank plus scaffolding gives finer adaptation than eight bands ever did, from a
quarter of the content.

**ASSUMPTION:** the runtime model can reliably pick and voice the right Door.
**IMPACT:** if it can't, the whole single-bank plan fails.
**IF WRONG:** children get Door 1 forever and 3-year-olds bounce.
**VALIDATE:** the Door is chosen *server-side* from the attempt count and sent
as an explicit instruction (`ask_mode=choice`), not left to model judgement.
This is the same lesson as ADR-0005 — decide it in code, hand the model a line
to say. Cheap to test: 20 transcripts, count Door escalations that fired.

### M2 — Earn the Answer (solves requirement 4)

Three verdicts replace today's `correct | wrong | revealed`:

| Verdict | Meaning | Clears the question? |
|---------|---------|----------------------|
| `solo` | Answered at Door 1 or 2, unaided | **Yes** |
| `helped` | Answered at Door 3, after being walked to it | **No** — returns tomorrow |
| `missed` | Never produced the answer, even at Door 3 | **No** — returns tomorrow |

The one-line bug fix: `CLEARED_RESULTS` drops `revealed`. Today a revealed
answer *clears the question*, which is why nothing ever repeats.

**Level mastery rule:** a level is **PASSED** only when all ten are `solo`.
Anything else leaves it OPEN and it reopens tomorrow with **only the unfinished
questions**, each asked via a **different Door than last time** — because the
alternative is a child hearing the identical sentence for four days and
memorising a sound rather than learning a fact.

### M2a — Teach In One Breath (the missing step)

> **Full spec:** [quizzy-doors.md](quizzy-doors.md) → *Door 3 in detail*. Not a
> separate system — Door 3 **is** the micro-teach.

**Today Quizzy does not teach. It tells.** The documented flow is two wrong
tries → *"reveal the answer warmly"*. A child hears the right answer and moves
on. Nothing explains, nothing connects, nothing sticks — which is exactly why a
repeat-until-mastered rule would otherwise just drill the same unexplained fact.

Door 3 must carry a **micro-teach**: the answer plus *one* reason, in **12–18
words**, then the child says it back.

> ❌ *"It's eight! Well done for trying."*
> ✅ *"Eight — four on each side, so they can walk on webs. How many legs, then?"*

Rules:
- One breath. A voice explanation longer than ~15 words is not heard by a
  6-year-old; it is waited through.
- The *why* must be concrete and physical, never abstract.
- Always end by handing the question back. The child speaks last.
- The micro-teach text is **authored per question**, not improvised — a new
  `teach_text` column on the bank. A 31B model improvising explanations to
  children is exactly the hallucination surface ADR-0005 closed.

> **Starting value: 12–18 words.** Test: read 10 micro-teaches aloud to 5- and
> 8-year-olds; ask them to repeat the reason back. Pass if ≥70% recall the
> reason unprompted. If they can't, cut to 10 words, not add more.

**This is a content obligation as large as the questions themselves** — every
bank row needs one. Budget for it before committing to requirement 4.

### M3 — The Anti-Trap Rule (P5)

**This is not optional.** A shared bank plus "repeat until solo" can strand a
3-year-old on level 3 forever, and requirement 4 taken literally guarantees it.

After **3 consecutive days** on the same level, the level is marked
**PRACTISED**, the child advances, and the unfinished questions go into a
**spaced-repetition pool** — re-entering as bonus questions 7 days later, at a
lower Door. Nothing is lost; nothing is a wall.

> **Starting value: 3 days.** Test: track the distribution of days-per-level
> across 50 devices for two weeks. Pass if fewer than 10% of level attempts hit
> the cap. If more than 25% hit it, the bank's difficulty spread is wrong —
> fix the content, not the cap. If under 2%, raise to 4 days.

### M4 — The Wonder Question (the intrinsic engine)

At the end of every level — passed or not — the child gets to **ask Quizzy one
question, about anything**, and Quizzy has to answer it.

This is unscored, uses no bank, and is the single highest-leverage mechanic in
this document. Rationale: the article's finding that the top performers all
activate **CD3 (Creativity/Empowerment)** and **CD4 (Ownership)**, which produce
"the kind of motivation that keeps working even after the game is turned off."
Every existing Quizzy mechanic is CD2. This one flips the power dynamic: for
thirty seconds the child is the examiner. It is also the thing they will
describe to their parents, which makes it the marketing mechanic too (§8).

Guardrails: the existing persona safety rules apply; "I don't know, but let's
wonder about it" is a *legitimate and good* answer, and models honest curiosity.

### M5 — Spark Streak, with mercy (CD8, tuned for children)

Daily streak, named aloud every session ("that's five days in a row!").
Duolingo's loss-aversion streak is the proven habit mechanic — but applied
unmodified to a 5-year-old it punishes children for their parents' schedules.

**Mercy rule:** one missed day per rolling 7 is forgiven automatically, narrated
as a gift rather than a technicality — *"you missed yesterday, so I kept your
spark warm for you."* Loss avoidance that a child cannot control is just anxiety.

> **Starting value: 1 forgiveness / 7 days.** Test: 14-day return rate vs. a
> no-mercy cohort. Pass if return rate is equal or better and streak-break
> churn drops. Adjust upward if 7-day retention is under 40%.

### M6 — The Reward Beat (CD4, voice-native)

A cleared level cannot show a badge — there is no screen. So the reward is
**audible and ownable**: each PASSED level unlocks one beat of a continuing
serial (Quizzy's own ongoing story), plus a silly sound effect that **the child
names**. The name is stored and reused: *"ready for the Blorp again?"*

Naming is ownership; ownership is CD4; CD4 is what survives the toy being put
down. A number the child cannot see is not a reward.

### M7 — "How did you know?" (teaches thinking, not recall)

On roughly one question in three, after a `solo`, Quizzy asks **"how did you
know that?"** There is no right answer and it is never scored. It is always
met with interest.

This is the only mechanic here that targets *transferable* skill — the article's
point that the best games teach "pattern recognition, persistence, curiosity, not
just content." It costs one extra turn and produces the reasoning-out-loud
behaviour that actually moves learning.

---

## 5. Five-Component Evaluation

Per the `game-design` skill's filter. Conflict priority: Response > Clarity >
Satisfaction > Fit > Motivation.

| Component | Current Quizzy | After redesign | Notes |
|-----------|---------------|----------------|-------|
| **Clarity** — can the child predict what happens? | **Weak.** Level state is invisible; a child cannot tell why a question came back. | **Strong.** Every session opens by naming the level and the streak; Door escalation is audible ("let's try it another way"). | Telegraph before resolution: Door 2 *announces* that it's an easier ask. |
| **Motivation** — does the outcome matter? | **Weak.** Advancing a level number the child can't see. | **Strong.** Wonder Question + named sound + serial story beat all persist across sessions. | Three persistent-state rewards where there was one invisible one. |
| **Response** — do inputs matter? | **Medium.** Answers are judged, but a wrong answer and a revealed answer had identical consequences. | **Strong.** Three distinct verdicts with three distinct futures. | Highest-priority component, and the one the `revealed` bug was silently breaking. |
| **Satisfaction** — does success feel earned? | **Weak.** Same praise for a guessed answer and a reasoned one. | **Strong.** Door 1 solo gets the biggest reaction; Door 3 gets a real but smaller one. | Two feedback channels minimum, and voice-only means both are audio: **words** (Quizzy's reaction) + **sound** (the named effect). |
| **Fit** — does it match identity? | **Strong** already. | **Strong.** Warm, curious, patient. | The Wonder Question is arguably *more* in-character than the quiz was. |

**Biggest single win:** Response. Fixing what `revealed` means is one line of
code and repairs the component the whole rubric prioritises first.

---

## 6. State Machine — Question Lifecycle

Required by the skill's checklist for any state-changing feature.

| Property | Definition |
|----------|------------|
| **States** | `UNSEEN → ASKED(door 1..3) → {SOLO, HELPED, MISSED}`; level: `OPEN → {PASSED, PRACTISED}` |
| **Entry conditions** | A question is `ASKED` only if it is in today's served set and not already `SOLO`. |
| **Exit conditions** | Verdict recorded, or the turn is cancelled (child interrupts / session drops). |
| **Interruptibility** | Child interrupt mid-question → **no verdict written**. The existing bridge already only persists MEMO on a completed final reply — preserve that; an unjudged answer must never advance state. |
| **Chained actions** | `SOLO` → maybe M7 ("how did you know?") → next question. `HELPED`/`MISSED` → next question, same day. |
| **Resource cost** | One of ten daily slots per asked question. Doors within a question cost **no** extra slot. |
| **Edge cases** | Bank unreachable → no scored play, free chat (ADR-0005 unchanged). Session drops mid-level → resume from the answer log, never from model memory. Two children one device → known limitation, `kid_id` already on the answer log. Child answers before the question finishes → accept it. Level has fewer than 10 authored rows → treat authored count as the level size, warn at the frontier. |

---

## 6a. What The Database Actually Records — And Doesn't

**Audited against the live schema. The answer is: not enough for any of this.**
`quiz_question_answer` holds `device_mac, kid_id, question_id, result,
answered_at`. That is all.

| You want to know | Recorded today? | Detail |
|---|---|---|
| **What the child actually said** | ❌ **No** | The utterance is never written to the answer log. It exists only in the raw transcript — and [ADR-0006](../adr/0006-raw-transcript-expires-durable-memory-does-not.md) **resets that transcript on a session gap**. The child's own words are on a short clock, then gone forever. |
| **Tries per level** | ⚠️ **Partly — more than first assessed** | The answer log is append-only and a re-asked question produces a *second row*, so cross-day attempts **are** countable. [parent-app-quiz-analytics-api.md](../../../cheeko-backend/main/manager-api-node/docs/parent-app-quiz-analytics-api.md) already ships this as `levels[].attempted`, and documents that *"a ten-question level can legitimately report `attempted: 12, correct: 8, wrong: 4`."* What is **not** recorded is tries *within* one question in one sitting. |
| **What was wrong** | ⚠️ **Structurally yes, practically no** | `result='wrong'` exists and is exposed to the parent app. But ticket 006 line 82: *"every question ends `correct` or `revealed`, so `result=wrong` will rarely be emitted and `counts.wrong` stays near zero."* The column works; the **flow never populates it**, because two wrong tries resolve to `revealed` instead. |
| **Which Door / how it was asked** | ❌ No | Doesn't exist yet — new in this design. |
| **STT confidence** | ❌ No | Nothing anywhere records that the child may have been misheard. |
| Which question, cleared or not | ✅ Yes | Append-only, one row per answered attempt. |
| Current level, levels completed | ✅ Yes | Derived, never stored (ADR-0005). Correct; keep. |

**Correction to an earlier reading:** attempts are *not* invisible. The
append-only log already counts them across days, and the parent app already
renders that number. The genuine gaps are narrower and specific: **the
utterance, the within-question tries, the Door, and STT confidence.**

That still blocks this design. M1 can't be evaluated without knowing which Door
worked, M2's solo/helped split is exactly a within-question distinction, §6b's
phonetic layer can't be tuned without `said_raw`, and M3's 3-day cap test plan
needs per-question attempt counts. Ticket 006's *"future flow that logs interim
attempts"* is this design — but it is an addition to a partly-working record,
not a rescue of a broken one.

### The addition — one table

```sql
CREATE TABLE quiz_question_attempt (
  id            BIGSERIAL PRIMARY KEY,
  device_mac    VARCHAR(20) NOT NULL,
  kid_id        BIGINT,
  question_id   BIGINT NOT NULL REFERENCES quiz_question(id) ON DELETE RESTRICT,
  attempt_no    SMALLINT NOT NULL,        -- 1,2,3… within this question, this day
  door          SMALLINT NOT NULL,        -- 1=open 2=choice 3=guided
  said_raw      TEXT,                     -- what STT heard, verbatim
  said_norm     TEXT,                     -- after normalisation (§6b)
  verdict       VARCHAR(10) NOT NULL,     -- solo | helped | missed | wrong
  stt_confidence REAL,
  attempted_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_qqa_device_question ON quiz_question_attempt(device_mac, question_id);
CREATE INDEX idx_qqa_kid_time         ON quiz_question_attempt(kid_id, attempted_at);
```

Design rules for it:

- **`quiz_question_answer` stays exactly as it is.** It remains the single
  source of truth for *cleared*, so ADR-0005's "progress is derived from the
  answer log" survives untouched. The attempt table is an **observation log**,
  never read by `deriveLevelState`. Two tables, two jobs — the same reasoning
  `banks.js` used to keep riddles out of the quiz.
- Tries-per-level becomes a real query: `COUNT(*) FROM quiz_question_attempt`
  grouped by level. That is the number requirement 4 is judged on.
- **`said_raw` is a verbatim recording of a child's speech.** It is the most
  sensitive column in your database. Give it a retention window in the same
  spirit as ADR-0006 — 30 days is a defensible starting position — and decide
  deliberately whether parents can see it. Do not let it default to forever
  because nobody chose.

**ASSUMPTION:** one row per attempt at current volumes is affordable.
**IMPACT:** ~3× the write rate of the answer log.
**IF WRONG:** cost and table growth.
**VALIDATE:** current answer-log rows/day × 3. Trivial to check before building.

---

## 6a-2. Reward Persistence — The Gap In This Document

**§4 designs four reward mechanics and, as first written, specified storage for
none of them.** Rewards are the half of "progress" that the existing schema does
not cover: `quiz_question_answer` records what was *answered*, nothing records
what was *earned*.

### The bug this nearly shipped

`PruneStaleStateFiles` deletes any file in `memory/state/` whose `date=` is older
than `quizStateMaxAge = 48 * time.Hour`
([quiz_state.go:26](../../pkg/livekit/quiz_state.go)). So if the streak or the
child's named sound lived in the workspace state file — the obvious place, since
that is where quiz state already lives — **it would silently vanish after any
two-day gap.** The child who most needs the hook is exactly the one who gets
their sound erased.

**Rule: no reward state in `memory/state/`. Ever.** That directory is
deliberately ephemeral. Rewards are durable, child-owned facts and belong in the
database, per [ADR-0008 — the child owns learning state](../adr/0008-the-child-owns-learning-state.md).

### What each mechanic needs

| Mechanic | Durable state | Home |
|---|---|---|
| **M5 · Spark Streak** | *nothing* | **Derive it from `quiz_question_answer`** — see §6a-3. No new column, no migration, kid-scoped for free. |
| **M5 mercy rule** | *nothing* | Also derived: bridge a single-day gap inside the trailing 7 days. Pure function. |
| **M6 · named sound** | `{slot: "Blorp"}` | **`kid_learning_progress.metadata`** with `subject='quizzy_reward'`, `topic='named_sounds'`. The table is unique on `(kid_id, subject, topic)` and `metadata` is `Json` — no migration needed. |
| **M6 · story beat position** | an integer | Same row, `topic='story_beat'`. Derivable from levels-passed, so store nothing if it stays 1:1. |
| **M4 · Wonder Questions** | a log, not state | **One new small table** (below). It is the only genuinely new storage this design needs. |
| **M7 · "how did you know?"** | none | Deliberately unscored and unlogged. Leave it. |

Three of five reuse tables that already exist. That is the point — a reward
system is not a schema project.

### The one new table

```sql
CREATE TABLE kid_wonder_question (
  id           BIGSERIAL PRIMARY KEY,
  kid_id       BIGINT,
  device_mac   VARCHAR(20) NOT NULL,
  asked_text   TEXT NOT NULL,          -- what the child asked, verbatim
  answered_ok  BOOLEAN,                -- did Quizzy manage a real answer
  asked_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_kwq_kid_time ON kid_wonder_question(kid_id, asked_at);
```

It earns its place three times over: it is the parent-facing artifact most
likely to drive retention (§8), the strongest signal for what to author next,
and the safety audit trail for M4. But `asked_text` is a **verbatim record of a
child's private curiosity** — give it a retention window and a deliberate
decision about parent visibility, the same way ADR-0006 handled the transcript.
Do not let it default to forever because nobody chose.

---

## 6a-3. The Streak Must Follow The Child — And Needs No Migration

`analytics_streaks` is keyed on **`mac_address`** — the *device*, not the child.
[ADR-0008](../adr/0008-the-child-owns-learning-state.md) and the in-flight
`feat/child-owned-state` work both say learning state follows the **child**. A
streak owned by the toy breaks the moment a sibling plays, and *"your twelve-day
streak!"* said to the wrong child is worse than no streak at all.

### Rejected: migrate `analytics_streaks` to carry `kid_id`

This was the first plan. It is worse than it looks:

- **`(mac_address, streak_type, streak_date)` is a live unique constraint** used
  by `logStreak` (`analytics.service.js:412`) for *every* streak type, not just
  quiz. Widening it changes a shared analytics path — the same
  fourth-consumer mistake as the parent-app API.
- **Nullable `kid_id` breaks uniqueness.** Postgres treats NULLs as distinct, so
  `UNIQUE (mac, type, date, kid_id)` would permit unlimited duplicate
  device-only rows. It needs an expression index over `coalesce(kid_id, 0)`,
  which Prisma can't express — a raw-SQL migration on a shared table.
- **Backfill has no correct answer.** Historical rows genuinely don't know which
  child earned them on a shared device. Any backfill is a guess written as fact.

### Chosen: derive the streak from the answer log

`quiz_question_answer` **already carries `kid_id`**, and it already has the exact
index this needs — `idx_quiz_answer_kid_time` on `(kid_id, answered_at)`, which
is purpose-built for this query and is sitting unused for it today.

```sql
-- the child's active days, most recent first
SELECT DISTINCT (answered_at AT TIME ZONE 'Asia/Kolkata')::date AS play_date
FROM quiz_question_answer
WHERE kid_id = $1
ORDER BY play_date DESC
LIMIT 14;                      -- 7 for the streak + 7 of headroom for mercy
```

Walk that list in code: consecutive dates extend the streak; **one** single-day
gap inside the trailing 7 is bridged (the mercy rule); a second gap, or any gap
of 2+ days, ends it.

Why this is the right call:

- **Zero migration, zero new columns, zero shared-table risk.**
- **Kid-scoped for free** — the ownership problem disappears rather than being
  solved.
- **Consistent with ADR-0005**: progress is derived from the answer log, never
  stored. A stored streak counter is exactly the kind of duplicate state that
  ADR chose to avoid, and it can drift from the log; a derived one cannot.
- The mercy rule needs no "forgiveness spent" record at all.

**Timezone matters here and nowhere else does.** A streak is the only mechanic
where "which day was that?" is user-visible, so the date cast must use the same
zone the prompt uses — `Asia/Kolkata`, matching `promptTimeBand`
([quiz_state.go:588](../../pkg/livekit/quiz_state.go)). Casting in UTC would
break streaks for evening play after 18:30 IST, which is peak usage.

### Two consequences to accept

1. **`kid_id` is nullable in the answer log.** Rows written before a child
   profile existed have none. Fall back to the device-scoped count for those,
   and *do not narrate a streak* when falling back — an unreliable number said
   confidently is worse than silence.
2. **A day only counts if the child answered something.** Opening the toy and
   saying nothing does not extend the streak. That is the correct reading of "you
   played", and it is also unexploitable.

`analytics_streaks` keeps doing whatever it does today. This design simply
doesn't use it.

---

## 6b. STT Errors — Yes, This Is A Real Problem, And It's Already Known

You are right to ask. It is documented and unfixed. Ticket 006, lines 77–80:

> ***"Caveat a prompt cannot fix:*** *STT runs before the judge. With the session
> language set to English, Sarvam may garble "pathu" before the model ever sees
> it. Check the raw transcript to separate an STT failure from a judging failure."*

**Today, every bit of fuzzy tolerance lives in the model's judgement** plus the
`accepted_answers` array and a prompt rule added on 2026-08-04 (*"judge the
meaning, not the language… the listed alternatives are examples, not the
complete set"*). There is **no normalisation, no phonetic matching, no
confidence check anywhere in code.** A child who is right but misheard is marked
wrong, and today nothing records that it happened.

This matters far more under requirement 4 than it did before: previously a
mis-scored answer cost one question. Now it costs the child **another whole day
on the same level.** An STT error becomes a punishment.

### The mitigation, in three layers

**Layer 1 — Normalise before judging (code, cheap, deterministic).**
Lowercase, strip punctuation, collapse whitespace, map spoken number words to
digits both ways (`"eight" ↔ "8"`, `"ate" → 8`), strip filler (`"um, I think
it's eight"` → `"eight"`). Pure function, unit-testable, no model involved. Most
"spelling mistakes" from STT are this shallow.

**Layer 2 — Phonetic near-match on a miss.** Before recording a miss, compare
`said_norm` against `answer_text` and every `accepted_answers` entry with
Double Metaphone plus a small edit-distance tolerance. `"ate"/"eight"`,
`"for"/"four"`, `"grate"/"great"` all collapse. On a phonetic hit, treat it as
correct and **log the near-match** so you can see how often STT was the problem.

> **Starting value: Levenshtein ≤ 2 on words of 5+ characters, phonetic key must
> match exactly.** Test: replay 200 logged `said_raw` values that were marked
> wrong; measure how many were actually right. Pass if the layer recovers >60%
> of them with zero false accepts on genuinely wrong answers. False accepts are
> worse than misses — a child credited for a wrong answer learns nothing.

**Layer 3 — Door 2 is the STT circuit-breaker.** This is an unplanned benefit of
M1 worth stating explicitly: a **binary choice is dramatically more robust to
garbled audio than an open answer.** "Six or eight?" only needs to distinguish
two known tokens; "how many legs?" must recognise arbitrary speech. So the Door
escalation doubles as automatic STT degradation handling — the child who keeps
getting misheard gets moved to the ask format that tolerates it.

**Never punish silently.** If STT confidence is below threshold, Quizzy asks
again warmly (*"I didn't quite catch that — say it once more?"*) and that
re-ask **does not count as an attempt**. A machine failure must never consume a
child's try.

---

## 7. Risks & Abuse Cases

| Risk | Severity | Mitigation |
|------|----------|------------|
| **Door 2 is a coin flip.** A child guesses "eight" with no knowledge and gets `solo`. | High | Door 2 only reachable *after* a Door 1 failure, and a Door-2 solo is recorded distinctly — it clears the question but the level's mastery report marks it assisted. Don't let a 50/50 look like knowledge. |
| **Shared bank bores older children.** | High | The 8–10 cohort clears levels fast and hits the authored frontier first. Mitigation is authoring depth, plus Door 1 being genuinely open-ended. **Flagged as the main product risk of requirement 1.** |
| **Repeat-until-solo teaches the sound, not the fact.** | High | Different Door each day (M2) + spaced repetition (M3). A child who memorises "eight" from a Door 3 count-along still has to produce it at Door 1 later. |
| **Wonder Question goes somewhere bad.** | High | Existing safety persona rules; refuse-and-redirect is already a solved path. Log the questions — they are also your best content-authoring signal. |
| **Streak anxiety.** | Medium | M5 mercy rule; never use fear language. |
| **The 3-day cap becomes the norm.** | Medium | Watch the cap-hit rate (M3 test plan). A high rate means the bank is too hard, not that the cap is wrong. |
| **Cost per turn rises.** | Medium | M7 and Doors add turns. Voice cost is real (`docs/cheeko-costing-sheet.xlsx`). Budget a per-session turn ceiling before shipping. |

---

## 8. Marketing & Positioning

You asked for this alongside the design, and one mechanic here *is* the
marketing.

**The demonstrable moment.** Every kids' ed-tech product markets with a screen
recording. You have no screen — so your asset is a **15-second audio clip of a
real child asking Quizzy a question and Quizzy answering well.** That is the
Wonder Question (M4). It is unfakeable, it is emotionally legible to a parent in
five seconds, and no competitor with a tablet can reproduce it. Design M4 partly
*because* it is the ad.

**Positioning against the field.** The article's ten are all screen products.
The category gap is not "another quiz app" — it is **screen-free learning time**,
which is the single thing parents of 3–7s actively want and cannot buy easily.
Lead with what the product *doesn't* have.

**Two-audience messaging:**
- To the child (in-product): "Quizzy wants to know what *you* think."
- To the parent (app, email, packaging): daily streak, levels passed, and —
  strongest of all — **the list of questions their child asked**. Parents will
  screenshot that. It is retention and referral in one artifact.

**Honest note on stats:** I have not verified current market-size or CAC figures
for children's audio-learning products, and the Numbers Policy forbids me
inventing them. Before committing spend, get: preschool ed-tech CAC benchmarks,
screen-time-concern survey data by parent age, and smart-toy category growth.
Treat every number above as a design rationale, not a market claim.

---

## 9. Creative Direction

**Story.** The Reward Beat (M6) needs a serial with roughly 40 beats — one per
level. Episodic, no cliffhanger anxiety, each beat standalone in ~20 seconds.
Suggested spine: Quizzy is collecting something across a world, and each level
passed finds one piece. Ownership hook: the child names the pieces.

**Music & sound.** This is your *entire* visual design budget, so spend it here.
Needed: a level-pass sting, a Door-escalation cue (warm, never a buzzer — the
sound of "let's try together", not "wrong"), a streak chime that grows subtly
with length, and a library of ~12 nameable silly sounds for M6. **No negative
sound anywhere in the game.** A voice-only product has no way to soften a harsh
tone with friendly visuals.

**Art.** Only where the product is *seen*: packaging, app, the parent report,
and the marketing asset. Quizzy's visual identity should be inferable from the
voice a child already knows — design the voice first, illustrate to match.

**Code.** See §10. The single most important creative constraint is §11's
guidance that game rules live in **code**, not in prompt prose.

---

## 10. Implementation Map

Ordered by ratio of impact to effort.

### Step 1 — Reverse the `revealed` decision (requirement 4)

**Correction to an earlier reading of this document: `revealed` clearing the
question is not a bug. It is a deliberate, documented design decision** —
[006-quizzy-prompt-cutover-e2e.md](../issues/quiz-bank/006-quizzy-prompt-cutover-e2e.md)
line 15: *"Two wrong tries → reveal the answer warmly, emit `result=revealed`
(**nothing blocks progression**)."* The original design chose flow over mastery
on purpose. Requirement 4 reverses that choice, which makes this a design
change needing an ADR, not a defect fix.

`manager-api-node/src/services/quiz.service.js:27`

```js
const CLEARED_RESULTS = ['correct', 'revealed'];   // before
const CLEARED_RESULTS = ['correct'];               // after
```

**Verify before shipping:** how many existing rows are `revealed`? Every one
reopens immediately and pulls children mid-progress backwards. Count first, then
decide on a grandfather cutoff.

**This one line also changes Riddler**, automatically — see §10a.

### Step 1a — Cross-character blast radius (audited)

**Cheeko and Nani are safe. Riddler is not — it changes automatically.**

| Character | Effect | Evidence |
|---|---|---|
| **Cheeko** | ✅ **No change** | Its greeting carries neither `{{QUIZ_QUESTIONS}}` nor `{{RIDDLES}}`, so `PromptWantsQuizBatch` is false → the speculative fetch is cancelled ([main.go:785](../../cmd/picoclaw-livekit/main.go)), `RenderQuizQuestions` returns the prompt untouched, and it never emits `type=daily_quiz`, so it never touches the shared state file. |
| **Nani** | ✅ **No change** | Same path. ADR-0006 also notes Nani reads only `USER.md`. |
| **Riddler** | ⚠️ **Changes automatically, whether you intend it or not** | `banks.js` `resolveBank` gives quiz and riddle **one shared service**. `CLEARED_RESULTS`, `deriveLevelState`, `ageBandFromBirthDate` and the day gate are all shared code. `riddle_question` has its own `age_band` column too. |

#### The parent app is a fourth consumer, and it is a published contract

`GET /toy/api/mobile/progress/quiz` is documented for app developers in
[parent-app-quiz-analytics-api.md](../../../cheeko-backend/main/manager-api-node/docs/parent-app-quiz-analytics-api.md).
It exposes, by name, **every field this design changes**:

- `banks[].age_band` — Step 2 collapses it
- `levels[].correct / wrong / revealed`, documented as *"Three outcomes, not two"*
- `questions[].result` — typed as `"correct" | "wrong" | "revealed"`
- `accuracy` = `correct / attempted`, `points` = `10 × correct`
- An entire section titled *"`revealed` is a third outcome, and it still advances
  the child"*, instructing the app that it *"needs its own tile and its own icon"*

So renaming verdicts to `solo | helped | missed` is **an external API break, not
an internal refactor** — and the very semantics being reversed are written into
the client's rendering instructions.

Required, not optional:
1. **Keep the wire contract stable.** Map new verdicts to the old three on
   response: `solo → correct`, `helped → revealed`, `missed → wrong`. The
   mapping is nearly semantic already.
2. **Add new fields additively** (`door`, `attempts_within_question`,
   `mastery: solo|helped|practised`). Never repurpose an existing field's meaning
   — an app in the wild will keep rendering the old one.
3. **Update the doc's §2 in the same PR.** It currently tells app developers that
   revealed *advances* the child. After Step 1 that is false, and a parent's
   dashboard will contradict what the toy did.
4. `accuracy` and `points` shift meaning once `revealed` stops clearing. Decide
   whether `helped` earns partial points before a parent notices their child's
   score dropped overnight for no visible reason.

#### The importer needs the new columns

`scripts/import-quiz-questions.js` requires headers
`code, age_band, level, question_text, answer_text` and upserts by `code`
(idempotent — good). It needs `teach_text` for M2a, and `age_band` becomes a
constant. Update the importer *before* the content merge, not after, or the
re-levelling sheet won't load.

**Consequences you must decide on before Step 1 ships:**

1. **Riddler inherits the mastery rule.** Riddles repeat until solved. That may
   be *wrong* for riddles — a riddle whose answer you already know isn't a
   riddle. Consider a per-bank `clearOnReveal` flag in `banks.js`. That file is
   already the right home for exactly this kind of per-bank difference.
2. **Collapsing age bands means re-levelling *both* banks**, not one. Double the
   content work in Step 2.
3. **`kid_learning_progress` topic strings break continuity.** The table is
   unique on `(kid_id, subject, topic)` where topic is `"<band> level <n>"`.
   Change the band value and *"6-8 level 2"* becomes *"all level 2"* — old
   achievement rows are orphaned, and a child's history shows a gap they didn't
   earn. Decide: migrate the strings, or accept the discontinuity and note the
   cutover date. Do not discover this in production.

### Step 2 — Collapse the bank (requirement 1)

- `quiz.logic.js` — `ageBandFromBirthDate` returns a single constant. Keep the
  function and the column; deleting them is a migration you don't need. Retiring
  a *value* is cheaper and reversible than dropping a *column*.
- `quiz_question.age_band` — set every active row to `'all'`; keep the
  `(age_band, language, level)` index as-is.
- **Content merge is the real work, not the code.** Eight banks → one ordered
  ladder means re-levelling every question by difficulty. Run `/content-audit`
  first to see what you actually have per level.

### Step 3 — Verdicts and Doors (M1, M2)

- `ANSWER_RESULTS` → `['solo', 'helped', 'missed']`, `CLEARED_RESULTS = ['solo']`.
  Map legacy values on read; do not rewrite history.
- Serve `ask_mode` per question from the server, computed from prior attempts —
  **the model must not choose the Door.** ADR-0005's lesson applies directly.
- Worker: extend the MEMO contract with the Door used. The existing verdict
  validation in `parseQuizVerdict` ([quiz_state.go:256](../../pkg/livekit/quiz_state.go)) is
  the right place, and its anti-invention guards must keep working — a Door 3
  paraphrase is *much* looser than today's asks, so re-check
  `questionTextMatchesBank` against real Door 3 transcripts before trusting it.

### Step 4 — Anti-trap (M3)

Days-on-level is derivable from the answer log — keep ADR-0005's
"progress is derived, never stored" rule. Add the spaced-repetition pool as a
query, not a table, if you can.

### Step 5 — Engagement layer (M4, M5, M6, M7)

Prompt + persona work, plus the reward persistence in §6a-2 — **one new table
(`kid_wonder_question`), everything else reuses `analytics_streaks` and
`kid_learning_progress.metadata`.**

Ship **M4 (Wonder Question) first and alone**: cheapest change, largest predicted
effect, and shipping it by itself is the only way you'll learn whether that
prediction was right.

Non-negotiable: **no reward state in `memory/state/`** — the 48h prune deletes
it. See §6a-2.

---

## 11. The Constraint That Governs All Of This

Game logic executed by a ~31B model holding state in prose has a hard ceiling.
Everything ADR-0005 learned still applies, and this redesign *increases* the
model's bookkeeping load. So:

- **Doors, verdicts, mastery, streaks, and the day gate are all computed
  server-side** and handed to the model as lines to say.
- The model voices, judges, escalates on instruction, and improvises only the
  unscored parts (Wonder Question, M7, encouragement).
- Anything the model must "remember" across turns goes in `memory/state/`, not
  chat history. This is already true and already load-bearing.

If a mechanic here requires the model to track more than two or three variables
unaided, it is misdesigned — push it to the server.

---

## 12. Playtest Plan

The skill's five required scenarios, adapted for voice:

1. **New player** — a 4-year-old who has never met Quizzy. Can they play without
   a parent explaining rules? *Pass:* completes 5 questions unassisted by the adult.
2. **Stress** — child interrupts every question mid-sentence, answers before
   the question ends, goes silent for 60s. *Pass:* no bogus verdict rows.
3. **Skill** — does a child who reasons out loud do better than one who guesses?
   *Pass:* Door-1 solo rate correlates with reasoning behaviour.
4. **Abuse** — can a child clear a level by saying "I don't know" ten times, or
   by guessing at Door 2 repeatedly? *Pass:* no, level stays OPEN.
5. **Readability** — can a *parent* overhearing tell what happened and why?
   *Pass:* they can state the level outcome without asking.

Plus two specific to this design:
6. **The 3-year-old floor** — does the youngest cohort reach a Reward Beat on
   day one? If not, the shared bank's first level is too hard and requirement 1
   needs an easier on-ramp.
7. **The 10-year-old ceiling** — how many days to the authored frontier?

---

## 12a. The Prompt Has Not Been Read

**Stated plainly: no part of this document is based on reading Quizzy's actual
prompt.** It is not in this repo. It lives in the Manager DB, and everything
above about current behaviour is inferred from
[ticket 006](../issues/quiz-bank/006-quizzy-prompt-cutover-e2e.md), ADR-0005 and
the worker code that consumes it.

Read it before implementing. Note the trap that ticket recorded — the row is
**`agent_code = 'quiz_master'`**, not `'quizzy'`; a query written from the
obvious guess matches zero rows and silently returns nothing:

```sql
SELECT agent_code, agent_name, length(system_prompt), length(greeting_prompt)
FROM ai_agent_template WHERE agent_code IN ('quiz_master','riddler');
```

Then dump `system_prompt` and check specifically:

1. **What §3's two-tries-then-reveal actually says** — does it explain at all, or
   only state the answer? M2a assumes the latter.
2. **Whether the day-gate / "refuse a second scored run" wording** survives the
   new mastery rule, or now contradicts it.
3. **Which parts hardcode `revealed` semantics** in the MEMO instruction.
4. **Whether the multilingual judging rule** (added 2026-08-04) needs extending
   for the Doors — Door 3's guided phrasing is far looser than today's asks, and
   `questionTextMatchesBank` in
   [quiz_state.go](../../pkg/livekit/quiz_state.go) must still recognise it.
   That guard exists because four invented questions once reached the database;
   re-verify it against real Door 3 transcripts before trusting it.

**Back up before any UPDATE.** Ticket 006's backup-and-show-the-diff procedure
is the one to copy; it caught two traps that would have caused real damage.

---

## 13. Open Questions

0. **Should Riddler inherit the mastery rule?** It will, for free, unless you add
   a per-bank flag. Repeating a riddle until solved may be actively wrong.
1. **Is requirement 1 worth its cost?** One bank means the same questions for a
   3- and a 10-year-old, carried entirely by M1's scaffolding. It's a real
   simplification with a real product risk. My read: worth it — age is a poor
   ability proxy and eight banks is an unfundable authoring load — but it is
   your call, and it is the one decision here that is hard to reverse once the
   content is merged.
2. **How many `revealed` rows exist today?** Determines whether Step 1 needs a
   grandfather clause.
3. **Per-turn cost ceiling?** Doors and M7 add turns. Need a number before Step 5.
4. **Does the Wonder Question need a parent-visible log?** Strong for retention,
   but it is a record of a child's private curiosity. Decide deliberately.
5. **Two children, one device.** `kid_id` exists on the answer log but the day
   gate is per-device. Out of scope here; will surface fast in households with
   siblings.

---

## 14. Next Actions

| Order | Action | Tool |
|-------|--------|------|
| 1 | **Dump and read the `quiz_master` prompt** (§12a) — nothing else is safe first | SQL |
| 2 | Count `revealed` rows and the level-pullback blast radius | SQL |
| 3 | Ship the attempt log (§6a) — **before** the mastery rule, so day one is measured | schema + code |
| 4 | Decide Riddler's `clearOnReveal` flag (§10 Step 1a) | `banks.js` |
| 4a | **Freeze the parent-app wire contract** and update its doc's §2 | API + docs |
| 4b | Add `teach_text` to the importer before any content re-levelling | `import-quiz-questions.js` |
| 5 | Ship `CLEARED_RESULTS = ['correct']` + STT Layer 1 normalisation together | code |
| 6 | Audit what the bank contains per level, both banks | `/content-audit` |
| 7 | Get real feel data before building the engagement layer | `/playtest-report` |
| 8 | Check difficulty spread for a merged single ladder | `/balance-check` |
| 9 | Write **ADR-0009** — single bank, mastery-over-flow, attempt logging | `docs/adr/` |
| 10 | Expand any mechanic into a full spec when it's next to build | `/gdd-system` |

**Why 3 comes before 5.** Requirement 4 makes every mis-scored answer cost a
child an entire day. Shipping the mastery rule without the attempt log means the
first time you learn it is hurting children is when a parent complains — you
would have no data showing which questions stall, how many tries they take, or
how often STT was the real culprit. Log first, then enforce.

**ADR numbering:** 0006 is taken (transcript expiry); this repo is at 0008. The
next free number is **0009**.
