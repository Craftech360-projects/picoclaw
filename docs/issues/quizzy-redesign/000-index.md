# Quizzy Redesign — issue index

Source: [quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) (master GDD),
[quizzy-doors.md](../../design/quizzy-doors.md) (system #15),
[quizzy-mastery-and-anti-trap.md](../../design/quizzy-mastery-and-anti-trap.md) (system #17).

Design is complete. These issues are the implementation.

## Order

**Phase A — facts that can invalidate the design.** Do not skip; both can change what
the rest of these tickets say.

| # | Title | Type | Blocked by |
|---|---|---|---|
| [001](001-read-quiz-master-prompt.md) | Dump and read the `quiz_master` prompt | HITL | — |
| [002](002-revealed-blast-radius.md) | Measure the `revealed` level-pullback blast radius | HITL | — |
| [003](003-adr-0009.md) | ADR-0009 — single bank, mastery over flow, attempt logging | HITL | 001, 002 |

**Phase B — instrument before you enforce.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [004](004-attempt-log.md) | Attempt log: table, write path, read-back | AFK | 003 |
| [005](005-freeze-parent-app-contract.md) | Freeze the parent-app wire contract | AFK | 003 |
| [006](006-riddler-clear-on-reveal-flag.md) | Riddler `clearOnReveal` flag | AFK | 003 |
| [007](007-importer-teach-text.md) | `teach_text` in the importer | AFK | — |

**Phase C — the mastery reversal.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [008](008-mastery-flip-and-stt-normalisation.md) | `CLEARED_RESULTS = ['correct']` + STT Layer 1 | AFK | 004, 005, 006 |

**Phase D — the Doors.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [009](009-server-computed-doors.md) | Server computes `ask_mode` / Door per question | AFK | 004, 008 |
| [010](010-worker-per-turn-door-injection.md) | Worker injects the Door per turn + MEMO carries it | AFK | 009 |
| [011](011-door-3-micro-teach.md) | Door 3 micro-teach + re-verify `questionTextMatchesBank` | AFK | 007, 010 |
| [012](012-anti-trap-days-on-level.md) | Anti-trap: `days_on_level` from the answer log | AFK | 004, 009 |

**Phase E — the bank collapse.** Hardest to reverse (§13 Q1).

| # | Title | Type | Blocked by |
|---|---|---|---|
| [013](013-collapse-age-band-code.md) | Collapse `age_band` to `'all'` (code side) | AFK | 003, 007 |
| [014](014-relevel-both-banks.md) | Re-level both banks onto one ladder (content) | HITL | 013 |

**Phase F — engagement.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [015](015-wonder-question.md) | M4 Wonder Question, shipped alone | AFK | 004 |

## Not ticketed, deliberately

**M5 (Spark Streak), M6 (Reward Beat), M7 ("How did you know?")** — §14 gates these on
real playtest data. A ticket written now would have no verifiable acceptance criteria.
Ticket them after `/playtest-report` on real child sessions.

## Standing constraints

- **Never rewrite the answer log.** Grandfather clauses are read-side predicates.
- **No reward or quiz state in `memory/state/`** — the 48h prune eats it (§6a-2).
- **Progress is derived, never stored** (ADR-0005). No `days_on_level` column.
- **The model must not compute Doors, verdicts, mastery, streaks or the day gate** (§11).
- **The parent app is a published API consumer.** Verdict renames are external breaks;
  map on the wire, add fields additively.
- **Content changes go to the dev DO box only — never prod.**

## Locked design decisions

Mastery bar: Door 1 **or** 2 unaided clears · anti-trap cap **3 days** · Riddler keeps
flow via `clearOnReveal` · spaced items are bonus-only, never block · all 3 Doors
available in one sitting · 1 attempt per Door · repeats reopen at Door 1 · distractors
**authored**.
