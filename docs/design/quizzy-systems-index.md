# Systems Index: Quizzy

> **Status**: Draft — produced by `/map-systems`, not yet reviewed
> **Created**: 2026-08-14
> **Source**: [quizzy-redesign-gdd.md](quizzy-redesign-gdd.md) + the live implementation
> (substituted for `design/gdd/game-concept.md`, which does not exist — this is a
> shipped product being decomposed retroactively, not a concept being planned)
> **Platform**: [.claude/docs/technical-preferences.md](../../.claude/docs/technical-preferences.md) — no game engine, voice-only

**Director gates**: TD-SYSTEM-BOUNDARY, PR-SCOPE and CD-SYSTEMS all skipped — Lean mode
(no `production/review-mode.txt`; lean is the documented default and none of the three
is a PHASE-GATE).

---

## Overview

Quizzy is a voice-only daily learning game for children aged 3–10, running on an
embedded toy with no screen. Its mechanical scope is unusually lopsided: there is
**no rendering, no input handling, no physics, no camera** — the entire
"presentation layer" is speech and sound, and the entire "input layer" is
speech-to-text. What would be trivial systems in a screen game (showing progress,
confirming an action) become genuinely hard, while whole categories of normal game
system simply don't exist.

The decomposition below finds **25 systems** (26 enumerated, one since folded
away), of which **13 already exist in
code** and 11 are new or absent. The critical structural insight: Quizzy's game
rules are split across two repositories and one *database-resident prompt*, and
the prompt is an undocumented third implementation surface. Several systems listed
here have no code at all — they live only as English sentences in
`ai_agent_template.system_prompt`.

The three bottleneck systems — **Question Bank**, **Progress Derivation**, and
**Verdict Reporting** — carry almost all the dependency weight and almost all the
risk. Every one of the redesign's five requirements routes through at least two of
them.

---

## Systems Enumeration

Category key: **Content** · **Selection** · **Judging** · **Progression** ·
**Reward** · **Persistence** · **Voice** (this game's UI layer) · **Meta**

| # | System | Category | Tier | Status | Depends On |
|---|--------|----------|------|--------|------------|
| 1 | Question Bank | Content | Now | ✅ Implemented | — |
| 2 | Content Authoring Pipeline | Content | Mastery | ⚠️ Partial (importer exists, no `teach_text`) | 1 |
| 3 | Bank Registry (quiz ⇄ riddle) | Content | Now | ✅ Implemented | — |
| 4 | STT Normalisation *(inferred)* | Judging | Now | ❌ **Absent** | — |
| 5 | Audio Feedback Library *(inferred)* | Voice | Engagement | ❌ **Absent** | — |
| 6 | Session State Persistence | Persistence | Now | ✅ Implemented | — |
| 7 | Question Selection / Level Derivation | Selection | Now | ✅ Implemented | 1, 3 |
| 8 | Progress Derivation | Progression | Now | ✅ Implemented | 1, 7 |
| 9 | Prompt Composition & Injection | Voice | Now | ✅ Implemented | 1, 6 |
| 10 | Attempt Observation Log *(inferred)* | Progression | Now | ❌ **Absent** | 1 |
| 11 | Answer Judging (LLM) | Judging | Now | ⚠️ Prompt-only, no code | 1, 4 |
| 12 | Anti-Invention Guards *(inferred)* | Judging | Now | ✅ Implemented | 1, 11 |
| 13 | Verdict Reporting (MEMO channel) | Judging | Now | ✅ Implemented | 11, 12 |
| 14 | Day Gate | Progression | Now | ✅ Implemented | 8 |
| 15 | Ask Scaffolding — the Doors | Selection | Mastery | 📝 **Designed** — [GDD](quizzy-doors.md) | 1, 4, 7, 9, 10 |
| ~~16~~ | ~~Micro-teach~~ | — | — | 🔀 **Folded into #15** — Door 3 *is* the micro-teach; two docs for one turn was the wrong seam | — |
| 17 | Mastery & Anti-trap | Progression | Mastery | 📝 **Designed** — [GDD](quizzy-mastery-and-anti-trap.md) | 8, 10, 13, 15, 18 |
| 18 | Spaced Repetition Pool | Progression | Mastery | ❌ **New (M3)** | 17 |
| 19 | Wonder Question | Reward | Engagement | ❌ **New (M4)** | 22 |
| 20 | Spark Streak | Reward | Engagement | ❌ **New (M5)** | 8 |
| 21 | Reward Beat & named sounds | Reward | Engagement | ❌ **New (M6)** | 5, 17 |
| 22 | Safety / Refusal Path | Meta | Engagement | ⚠️ Prompt-only | 9 |
| 23 | Analytics & Parent Reporting | Meta | Now | ✅ Implemented (**published API**) | 8, 10 |
| 24 | Failure / Degradation Path | Meta | Now | ⚠️ Partial (never live-tested) | 1, 9 |
| 25 | Cross-character State Isolation | Persistence | Later | ⚠️ Partial (device-scoped) | 3, 6 |
| 26 | Retention & Privacy Controls *(inferred)* | Meta | Later | ❌ **Absent** | 10, 19 |

**13 implemented · 6 partial · 6 absent · 1 folded into another.** Five of the six absent systems are
required by the redesign's stated requirements, not optional additions.

---

## Why the inferred systems are real systems

The skill's inference pattern, applied to a voice game:

- **"Judge a spoken answer"** implies **STT Normalisation (4)**. There is no
  keyboard — every answer arrives having already passed through a lossy channel.
  Today nothing normalises it; tolerance is entirely the LLM's judgement. §6b.
- **"Repeat until mastered"** implies **Attempt Observation Log (10)**. A rule
  about attempts cannot be tuned, tested, or proven without recording attempts.
  Ticket 006 already anticipated this: *"the value exists for a future flow that
  logs interim attempts."*
- **"A reward the child owns"** implies an **Audio Feedback Library (5)**. With no
  screen, the reward *is* a sound. A badge is not available as a design option.
- **"The child may ask anything"** implies a **Safety / Refusal Path (22)** —
  which exists, but only as prompt prose, with no test and no log.
- **"Record what the child said"** implies **Retention & Privacy Controls (26)**.
  `said_raw` and `asked_text` are verbatim recordings of a child's speech; storage
  without a deletion policy is a decision made by omission.
- **A curated bank** implies a **Content Authoring Pipeline (2)** that must
  outrun the fastest child — ADR-0005 accepted this obligation explicitly.

---

## Dependency Layers

**Foundation** — zero dependencies, everything else rests on these
> 1 Question Bank · 3 Bank Registry · 4 STT Normalisation · 5 Audio Library · 6 Session State Persistence

**Core** — depend only on Foundation
> 7 Question Selection · 8 Progress Derivation · 9 Prompt Composition · 10 Attempt Log · 11 Answer Judging

**Feature** — the game rules
> 12 Anti-Invention Guards · 13 Verdict Reporting · 14 Day Gate · 15 Doors (incl. the micro-teach) · 17 Mastery/Anti-trap · 18 Spaced Repetition

**Presentation** — this game's UI is speech
> 19 Wonder Question · 20 Streak · 21 Reward Beat · 22 Safety Path

**Meta / Polish**
> 23 Analytics · 24 Degradation Path · 25 Cross-character Isolation · 26 Retention Controls

### Bottlenecks — high dependency fan-out, therefore high risk

| System | Dependents | Why it's dangerous |
|---|---|---|
| **1 · Question Bank** | 2, 7, 8, 10, 11, 12, 15, 24 | Eight systems. Also the only one with an unbounded human content obligation — now doubled by the single-bank merge and **tripled** by `teach_text` **and** `distractor`, both required on every row in both banks. |
| **8 · Progress Derivation** | 14, 17, 20, 23 | Every requirement (one level/day, mastery, streak, parent report) reads it. Changing what "cleared" means changes all four at once. |
| **13 · Verdict Reporting** | 17, 23 | The narrowest pipe in the system: one regex-parsed line of model output carries all scoring. Door 3's looser phrasing directly threatens its anti-invention guards. |

### Circular dependency — one found, with a resolution

**15 Doors ⇄ 10 Attempt Log.** The Doors system needs the attempt count to decide
whether to escalate; the Attempt Log needs to record which Door was used. Read
naively this is a cycle within a single turn.

**Resolution — define the contract, don't abstract it:** the *server* computes
`attempt_no` and assigns `ask_mode` **before** the turn, from rows already
written; the log records what was assigned **after** the turn resolves. Read
before, write after, never negotiate within. This is the same discipline ADR-0005
applied to question selection — decide in code, hand the model a line to say —
and it keeps the model out of a decision it would make inconsistently.

### A structural finding the skill's layering exposed

**Systems 11 (Answer Judging) and 22 (Safety Path) have no code.** They exist
only as English in a database column. That makes the prompt a third
implementation surface alongside the Go worker and the Node API — untested,
unversioned in git, and (per §12a of the GDD) **not yet read by anyone doing this
redesign**. Every other system in this index can be reasoned about from the
repository. These two cannot.

---

## Priority Tiers

Retitled from the template's MVP/Vertical-Slice/Alpha ladder, which assumes a
pre-release project. Quizzy is live with real children, so the tiers are shipping
increments.

| Tier | Meaning | Design urgency |
|---|---|---|
| **Now** | Correctness, measurement, and contract safety. Nothing player-facing changes. | FIRST |
| **Mastery** | Delivers requirements 1–4. The level actually repeats until learned. | SECOND |
| **Engagement** | Delivers requirement 5. Where the product stops being a test. | THIRD |
| **Later** | Privacy, multi-child, analytics depth. | As needed |

### Tier: Now

| System | Why |
|---|---|
| 11 Answer Judging | **Read the prompt first.** Two systems live only here, and the redesign edits both blind otherwise. |
| 10 Attempt Log | Ship measurement *before* enforcement — otherwise the first signal that mastery is hurting a child is a parent complaint. |
| 23 Analytics | Freeze the published wire contract before verdicts change, or the parent app renders a lie. |
| 4 STT Normalisation (L1) | A mis-heard answer now costs a whole day, not one question. Must land with the mastery rule, not after. |
| 3 Bank Registry | Decide `clearOnReveal` — otherwise Riddler silently inherits a rule that may be wrong for riddles. |
| 13 Verdict Reporting | The `revealed` reversal. One line, but it is the hinge of requirement 4. |

### Tier: Mastery

| System | Why |
|---|---|
| 17 Mastery & Anti-trap | Requirement 4 itself — *and* the rule that stops it stranding a 3-year-old. Ship them together or not at all. |
| 15 Doors | The only reason one shared bank can serve ages 3–10. Requirement 1 depends on it entirely. |
| *(micro-teach)* | Folded into 15 as Door 3. Without it, "repeat until correct" drills an unexplained fact — teaching is what makes repetition legitimate. |
| 2 Content Pipeline | `teach_text` + re-levelling **both** banks. Largest non-code cost in the whole plan. |
| 18 Spaced Repetition | The exit valve that makes the 3-day cap honest rather than a silent write-off. |

### Tier: Engagement

| System | Why |
|---|---|
| 19 Wonder Question | Highest predicted effect, lowest cost, and it is also the marketing asset. **Ship alone, first, to measure it.** |
| 20 Streak | Habit formation — derived from the answer log, so it costs nothing structural. |
| 21 Reward Beat + 5 Audio Library | With no screen, this is the entire reward channel. The child names the sound; naming is the ownership hook. |
| 22 Safety Path | Must be hardened *before* 19 ships, not alongside it. |

### Tier: Later

25 Cross-character Isolation (per-character session scoping) · 26 Retention &
Privacy Controls · 23 Analytics depth (new additive fields)

---

## Recommended Design Order

1. **11 Answer Judging** — read and document the live prompt (blocks everything)
2. **23 Analytics** — freeze the contract
3. **10 Attempt Log** — measure before enforcing
4. **4 STT Normalisation** — L1 only
5. **13 Verdict Reporting** + **3 Bank Registry** — the `revealed` reversal, with Riddler decided
6. **17 Mastery & Anti-trap** + **18 Spaced Repetition** — as one unit
7. **15 Doors** (Door 3 carries the micro-teach) → **2 Content Pipeline**
8. **22 Safety Path** → **19 Wonder Question** (alone, measured)
9. **20 Streak** → **5 Audio Library** → **21 Reward Beat**
10. **25**, **26** when the above is stable

---

## High-Risk Register

| Risk | System | Note |
|---|---|---|
| Content obligation outruns the team | 1, 2 | Single bank + `teach_text` + both banks re-levelled. The largest cost here is not code. |
| Anti-invention guards break on Door 3 | 12, 13 | `questionTextMatchesBank` needs ≥1 shared content word. Door 3's guided phrasing may share none. Re-verify against real transcripts. |
| Published API contract breaks | 23 | Verdict rename is an external break. Map on the wire; add new fields additively. |
| The prompt is unread | 11, 22 | Two systems with no code and no reader. Highest-leverage unknown in this index. |
| Riddler inherits mastery silently | 3, 17 | Shared `resolveBank` means the change is automatic, not opt-in. |
| Degradation path never tested | 24 | Ticket 006's API-down criterion was carried to 007 and, per that ticket, never exercised. |
| Child speech stored without policy | 26 | `said_raw`, `asked_text`. Decide retention deliberately, not by default. |

---

## Progress Tracker

| System | GDD | Status |
|---|---|---|
| **17 Mastery & Anti-trap** | [quizzy-mastery-and-anti-trap.md](quizzy-mastery-and-anti-trap.md) | **Reviewed & revised** — `/design-review` found 5 blocking + 5 recommended + 2 NTH; all applied. Pending *independent* re-review. |
| **15 The Doors** | [quizzy-doors.md](quizzy-doors.md) | **Reviewed & revised** — 6 blocking + 4 recommended + 2 NTH; all applied. Pending *independent* re-review. |
| ~~16 Micro-teach~~ | [quizzy-doors.md](quizzy-doors.md) — *Door 3 in detail* | **Folded into #15** — no separate GDD |
| 18 Spaced Rep, 19 Wonder, 20 Streak, 21 Beat | [quizzy-redesign-gdd.md](quizzy-redesign-gdd.md) §4 | Designed, not specced per-system |
| 10 Attempt Log | same, §6a | Designed (schema drafted) |
| 4 STT Normalisation | same, §6b | Designed (3 layers, test plans) |
| 20 Streak persistence | same, §6a-3 | Designed (derived, no migration) |
| 1, 3, 6, 7, 8, 9, 12, 13, 14, 23 | — | Implemented, undocumented as systems |
| 2, 5, 11, 22, 24, 25, 26 | — | **Not designed** |

Next per this skill: `/gdd-system 17-mastery-and-anti-trap` — the highest-tier
undesigned system whose dependencies are all in the Now tier.
