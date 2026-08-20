# 10. Progression Flows; Mastery Is Only Recorded

Date: 2026-08-20

## Status

Accepted

Amends [ADR-0009](0009-mastery-over-flow-on-one-bank-measured-by-an-attempt-log.md).
Its decision 1 (a revealed answer does not clear a question) **stands**. What changes is
that clearing no longer gates the level, and that Riddler stops deriving levels at all.
Decisions 2 (one bank) and 3 (the attempt log) are untouched.

## Context

ADR-0009 made "mastered" and "may advance" the same predicate: `deriveLevelState` returns
the lowest level holding any uncleared question, so one unsolved question out of ten holds
a child on that level until the 3-day anti-trap fires. Three problems surfaced once the
banks were being played rather than reasoned about.

**The level gate is doing the mastery test.** A child who has solved nine of ten is not
meaningfully un-progressed, but the machinery treats them as if they had solved none. The
strictness a parent notices is not the honest record — it is the wall.

**The batch repeats content the child already answered.** A sitting is the whole level,
cleared questions included, because serving only the outstanding ones made sessions
collapse: clear nine of ten and the next day holds one question, after which the model ran
out and *invented* one (2026-08-15, and the id it walked to was a Level 2 question). The
fix was correct about the failure and wrong about the filler — it re-asks solved questions
rather than reaching for unasked ones.

**Riddler derives levels it does not use.** With `clearOnReveal: true` every riddle is
finished the moment it is heard, so the level machinery enforces no mastery. It only
orders difficulty, at the cost of a gate, a completion rule and an anti-trap that can
never meaningfully fire.

## Decision

### 1. A level clears at a threshold, not at every question

`deriveLevelState` and `countCompletedLevels` take a per-bank `levelClearSlack`: a level is
finished when at most that many of its questions remain unmastered. Quizzy and Ginti allow
**2 of 10**. The anti-trap drops from 3 days to **2**, and the unmastered carry grows from
2 to 3.

Progression becomes generous; the record does not move. `clearOnReveal` stays `false`, so
`quiz_question_answer` still distinguishes a question the child solved from one they were
told. That distinction is the thing ADR-0009 was protecting and the only thing a parent
report can ever be built from — advancing a child is a product decision, forgetting what
they struggled with is data loss.

### 2. The batch tops up from the NEXT level, never from cleared questions

`idsForLevel` fills a short batch from the following level's unasked questions instead of
re-asking cleared ones. Outstanding questions still come first, so mastery keeps priority
and the day's ten are never crowded out.

The 2026-08-15 invention bug is guarded by the property that actually mattered — the batch
is full whenever content remains — not by the particular filler that first achieved it. A
test asserts that property directly.

A child who answers next-level questions correctly clears them early. That is a head start,
not a leak: `deriveLevelState` still walks levels in order, and those questions are simply
already done when it arrives.

### 3. Riddler stops deriving levels

A per-bank `gatedLevels: false` makes selection serve the least-recently-unheard riddles in
level order, with no clearing gate, no level completion and no anti-trap. Level becomes a
difficulty ramp.

Riddler keeps `clearOnReveal: true`, which now means only "do not repeat this riddle".

## Consequences

**Quizzy and Riddler diverge further, and that is the intent.** Quizzy and Ginti record
mastery while flowing; Riddler is pure flow. The `BANKS` flags carry all three differences
(`clearOnReveal`, `levelClearSlack`, `gatedLevels`), so no character changes behaviour
because another one did.

**"Levels completed" no longer implies "everything mastered".** Any surface reporting
progress must read the answer log for mastery, not the level count. `countCompletedLevels`
already counted anti-trap-skipped levels as finished, so this widens an existing gap rather
than opening a new one — but it widens it from a rare escape hatch to the normal path.

**Champion replay arrives sooner**, because levels clear at 8 of 10. Growing the banks
matters more than it did.

**Accepted risk:** a child can now finish a bank having never solved up to 20% of it. The
attempt log and the answer log both record exactly which ones, and the carried bonus
questions bring them back as practice. If that share proves high in real data, the answer
is more authored content or a tighter slack — not a return to the wall.
