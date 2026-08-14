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
| [001](001-read-quiz-master-prompt.md) | Dump and read the `quiz_master` prompt | HITL | ✅ closed |
| [002](002-revealed-blast-radius.md) | Measure the `revealed` level-pullback blast radius | HITL | ✅ closed — no clause needed |
| [003](003-adr-0009.md) | ADR-0009 — single bank, mastery over flow, attempt logging | HITL | ✅ closed — [ADR-0009](../../adr/0009-mastery-over-flow-on-one-bank-measured-by-an-attempt-log.md) |

**Phase B — instrument before you enforce.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [004](004-attempt-log.md) | Attempt log: table, write path, read-back | AFK | ✅ closed |
| [005](005-freeze-parent-app-contract.md) | Freeze the parent-app wire contract | AFK | ✅ closed |
| [006](006-riddler-clear-on-reveal-flag.md) | Riddler `clearOnReveal` flag | AFK | ✅ closed |
| [007](007-importer-teach-text.md) | `teach_text` in the importer | AFK | ✅ closed |

**Phase C — the mastery reversal.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [008](008-mastery-flip-and-stt-normalisation.md) | `CLEARED_RESULTS = ['correct']` + STT Layer 1 | AFK | ⚠ 8/9 — prompt line needs you |

**Phase D — the Doors.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [009](009-server-computed-doors.md) | Server computes `ask_mode` / Door per question | AFK | ✅ closed |
| [010](010-worker-per-turn-door-injection.md) | Worker injects the Door per turn | AFK | ✅ closed |
| [011](011-door-3-micro-teach.md) | Door 3 micro-teach + re-verify `questionTextMatchesBank` | AFK | ⚠ 6/8 — blocked on 014 |
| [012](012-anti-trap-days-on-level.md) | Anti-trap: `days_on_level` from the answer log | AFK | ✅ closed |

**Phase E — the bank collapse.** Hardest to reverse (§13 Q1).

| # | Title | Type | Blocked by |
|---|---|---|---|
| [013](013-collapse-age-band-code.md) | Drop `age_band` (code side) | AFK | ✅ closed |
| [014](014-relevel-both-banks.md) | Re-level both banks onto one ladder (content) | HITL | ⚠ scaffolded — 480+480 to author |

**Phase F — engagement.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [015](015-wonder-question.md) | M4 Wonder Question, shipped alone | AFK | ⚠ built — needs the prompt to emit `wonder=` |

**Found in testing.**

| # | Title | Type | Blocked by |
|---|---|---|---|
| [016](016-report-unclear-turns.md) | The model must report UNCLEAR turns | AFK | ⚠ built — needs one session check |

## The one gate in front of everything else

**Nothing left can be finished without a real voice session.** Four items are waiting on
one:

| Ticket | Waiting for |
|---|---|
| 008 | the prompt line (needs a human to run the `UPDATE`) |
| 010 | cache hit ratio, and watching escalation reach Door 3 |
| 011 | real Door 3 transcripts — which also need 014's authored `teach_text` |

Everything below Phase D is either that session or content authoring.

## Not ticketed, deliberately

**M5 (Spark Streak), M6 (Reward Beat), M7 ("How did you know?")** — §14 gates these on
real playtest data. A ticket written now would have no verifiable acceptance criteria.
Ticket them after `/playtest-report` on real child sessions.

## Environment order — test local first

**Everything is built and verified against the local dev database first. Only then dev
(DB1), and only then prod.** No ticket here is done when it works locally; local is the
gate to *start* promoting it, not the finish line.

| Env | Supabase project | Used by |
|---|---|---|
| **local** | `shlrfpbqkfnxqcmuatvs`:6543 | local `manager-api-node` (`.env`) — **start here** |
| **dev (DB1)** | `tsiocygczplmnjpqmutc`:5432 | DO dev box `64.227.170.31`, pm2 `manager-api` |
| **prod** | separate | EKS — never touched from a dev session |

Schema and seed changes must be applied to **both** local and DB1; they are different
projects and drift silently.

### State as of 2026-08-14

| | local | DB1 | prod |
|---|---|---|---|
| `question_attempt` table | ✅ | ✅ | ✗ |
| `teach_text` / `distractors`, both banks | ✅ | ✅ | ✗ |
| 008 prompt line (repeats are deliberate) | ✅ | ✗ old line | ✗ never read |
| bank content authored for Doors 2 / 3 | ✗ | ✗ | ✗ |

Both migrations were applied to DB1 with `db execute` rather than `migrate deploy`, because
six migrations recorded in the database are missing from the tree and block `deploy`
outright. Both files are `IF NOT EXISTS`, so a later working `deploy` re-runs them harmlessly.

The DO box runs older code against DB1. The new columns are additive and Prisma selects
explicit column lists, so the running service is unaffected until it is redeployed.

### ✅ Closed 2026-08-14: DB1 prompt verified identical to local

`quiz_master` on DB1 is **byte-identical** to local — 12,439 / 2,428 chars, both fields.
Every 001 finding applies to DB1 unchanged. **Prod has not been read**, so re-run the dump
there before a prod prompt `UPDATE`:

```bash
node scripts/dump-agent-prompt.js ./prompt-backup-db1
```

`manager-api-node/scripts/dump-agent-prompt.js`. Reads `DATABASE_URL`, writes one file per
prompt field, exits non-zero on zero rows or an empty `system_prompt` — the two silent
failures quiz-bank ticket 006 hit. It also prints every `agent_code` in the table when it
finds nothing, which is what makes a wrong code visible instead of silent.

### ✅ Closed 2026-08-14: 002 measured, no grandfather clause

Across local and DB1: **55 answer rows, zero `revealed`, zero `wrong`, zero level
pullback.** Nothing to grandfather, so 008 ships the flip with no date predicate. 003 and
008 are unblocked.

## Promotion gate — before anything here reaches prod

Prod is the DigitalOcean managed cluster (`db-postgresql-blr1-93302…:25060/defaultdb`).
It has **not** been queried, by design; everything measured so far is local and dev only.

Re-run the blast radius against prod before promoting 008:

```bash
DATABASE_URL="<prod-url>" node scripts/quiz-revealed-blast-radius.js
```

- **Non-zero `revealed`** → real children are mid-progress on questions about to reopen.
  The grandfather clause goes back in and 002's decision is reopened.
- **Zero `revealed` on real children** → the reveal path is not firing at all, and
  requirement 4 reverses a decision that never takes effect. Bigger than the clause;
  settle it before shipping.

Also re-dump the prompt there before any prod prompt `UPDATE` — prod is a third copy and
has not been diffed against local/dev.

### Refreshing local from DB1

```bash
SRC_DATABASE_URL="<db1-url>" node scripts/copy-quiz-tables.js --yes
```

Replaces `quiz_question`, `quiz_question_answer`, `riddle_question`,
`riddle_question_answer` only. Users, devices, kids and `ai_agent_template` are **not**
touched, so a data refresh cannot clobber local credentials. Refuses to run if source and
destination are the same database, and resets the id sequences afterwards.

## Standing constraints

- **Never rewrite the answer log.** Grandfather clauses are read-side predicates.
- **No reward or quiz state in `memory/state/`** — the 48h prune eats it (§6a-2).
- **Progress is derived, never stored** (ADR-0005). No `days_on_level` column.
- **The model must not compute Doors, verdicts, mastery, streaks or the day gate** (§11).
- **The parent app is a published API consumer.** Verdict renames are external breaks;
  map on the wire, add fields additively.
- **Content changes go to the dev DO box only — never prod.**

## Character codes differ from the names — check twice

`agent_code` is never the name you hear: `quiz_master` is *quizzy*, `riddle_master` is
*riddler*. Both the GDD's SQL and this ticket set's first attempt queried `'riddler'` and
concluded the character did not exist. A wrong code matches zero rows **silently**.

```sql
SELECT agent_code, agent_name FROM ai_agent_template ORDER BY agent_code;
```

Eight characters: `calm_companion` (Chanda), `Cheeko`, `masti` (Masti), `quiz_master`
(quizzy), `riddle_master` (riddler), `science_buddy` (Tara), `story_explorer` (Nani),
`word_wizard` (Mitthu).

## Locked design decisions

Mastery bar: Door 1 **or** 2 unaided clears · anti-trap cap **3 days** · Riddler keeps
flow via `clearOnReveal` · spaced items are bonus-only, never block · all 3 Doors
available in one sitting · 1 attempt per Door · repeats reopen at Door 1 · distractors
**authored**.
