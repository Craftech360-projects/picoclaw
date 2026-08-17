# 017 — Production promotion

**Type:** HITL · **Status:** open — prepared 2026-08-15, not executed

**Deploy boundary.** Every step below runs against production. Per
[`cheeko-deploy-boundaries`], these are Rahul's to run. The 2026-08-03 EKS deploy was a
one-time grant, not standing permission. An agent may prepare SQL, verification queries
and rollout order; it may not execute any of it.

## What is being promoted

| repo | `main` | rollback tag |
|---|---|---|
| picoclaw | `71ea27a` Merge branch 'docs/quizzy-redesign-issues' | `pre-quizzy-merge` → `2e2a6b2` |
| cheeko-backend | `6f1b3150` Merge branch 'feat/quizzy-attempt-log' | `pre-quizzy-merge` → `5b35d55` |

Both tags are annotated, pushed, and carry their own warnings. Deploy the merge commits;
the tags are the anchors.

The change set: mastery over flow (008), the Three Doors ladder (009–011), the attempt
log (004), the anti-trap day cap (012), the collapse to one bank (013), the Wonder
Question (015), UNCLEAR turn reporting (016), and the admin panel rebuilt around the new
stats. Verified across a full day of real voice sessions on the dev box.

---

## ✅ Step 0 ran 2026-08-17 — read-only. Result: NO-GO, and the reason is not the code

| | verified on prod |
|---|---|
| Services | `manager-api` + `mqtt-gateway` on 139.59.7.72, branch `main` @ `67c6a806`, clean tree, 6d uptime |
| Database | DigitalOcean managed `defaultdb` — reachable, healthy |
| **Prisma** | **`SKIP_DB_SYNC=1`. Migrations never run on boot. 26 in the tree, 0 applied. The schema was built by hand** |
| Quiz schema | **present**, but the OLD shape: `age_band` still there, values `3-5 / 6-8 / 9+`, 3 levels each, 90 active |
| Absent | `question_attempt`, `kid_wonder_question`, `device_kid_assignment`, `teach_text`, `distractors` |
| Prompt | `quiz_master` 12,439 / 2,428 — the pre-008/015/016 text. Dev is 13,835 |
| **Real usage** | **71 children, 60 devices, 47 answer rows** |
| Blast radius | **0 `revealed`, 0 `wrong`** ✅ |
| EKS | **unreachable from this machine** — AWS token invalid, so `kubectl` cannot authenticate |

**The 2026-08-05 record in [quiz-bank/008](../quiz-bank/008-production-promotion.md) is out of
date.** It said prod had no `quiz*` tables. It has them — someone applied the bank by hand.
That is also the root of the problem below.

### Why this cannot proceed without a decision

`SKIP_DB_SYNC=1` is the only reason prod boots. It makes `runPrismaMigrations()` a no-op,
which is why 26 unapplied migrations sit harmlessly beside a schema that already exists.
Three paths, two of them break production:

1. **Deploy the code, keep the flag.** Migrations still never run. The new code queries
   `question_attempt`, `teach_text` and `distractors` against a schema that has none of
   them — every quiz call errors, for all 71 children.
2. **Clear the flag so migrations apply.** `migrate deploy` starts at
   `20260124000000_init` against a populated database, hits "already exists", records a
   failed migration — P3009 — and `server.js` exits code 1. **Production goes down and
   stays down**, because every restart repeats it.
3. **Baseline first.** `migrate resolve --applied` for all 26 existing migrations, then
   apply the 5 new ones. This is correct, one-way, and runs against a live database with
   71 real children on it.

Path 3 is the only one, and it needs a snapshot and a window — not a deploy step.

### What is genuinely clear

**Blast radius is zero on prod.** 47 answer rows, no `revealed`, no `wrong`. So 002's
decision holds here too: the mastery flip ships with no grandfather clause. That gate is
closed.

### Content is a second, separate hazard

Prod carries 90 questions across three age bands. The redesign needs 240 on one bank.
**`copy-quiz-tables.js` must NOT be used** — it replaces the *answer* tables as well, and
prod's 47 answer rows belong to real children. A question-tables-only import is required,
and it has not been written.

### Blocked on Rahul

- A database snapshot, and a window to baseline 26 migrations
- EKS credentials, or the worker rollout run by someone who has them
- A decision on the content import: rewrite 90 rows in place, or import 240 and retire
  the old ones by `active = false`

---

## Step 0 — the survey, for reference

**The rest of this document branches on what you find, and the records say the branch is
the expensive one.**

Two earlier promotion tickets are still `open`, never executed:

- [quiz-bank/008](../quiz-bank/008-production-promotion.md) — as of 2026-08-05, prod had
  **no `quiz*` tables at all** and Quizzy sat at `cut_over=false` with an 8,312-char
  prompt containing no `{{QUIZ_QUESTIONS}}`.
- [per-age-banks/007](../per-age-banks/007-production-promotion.md) — never run.

If that is still true, **this is not an upgrade. It is the first quiz deployment to
production, with the redesign on top of it.** Everything below assumes you confirm which
world you are in first.

Read-only, no writes:

```sql
SELECT table_name FROM information_schema.tables
 WHERE table_schema = 'public' AND table_name LIKE 'quiz%' OR table_name LIKE 'riddle%';

SELECT count(*) FROM _prisma_migrations;              -- see the migration question below
SELECT agent_code, length(system_prompt), length(greeting_prompt)
  FROM ai_agent_template WHERE agent_code IN ('quiz_master','riddle_master');
```

```bash
DATABASE_URL="<prod>" node scripts/quiz-revealed-blast-radius.js
DATABASE_URL="<prod>" node scripts/dump-agent-prompt.js ./prompt-backup-prod
```

Record the answers in this file before proceeding.

### The migration question is the biggest single risk

`server.js` runs `prisma migrate deploy` **on boot**. Whatever is unapplied in the tree
applies automatically, in one go, the moment the new manager-api starts.

The tree now carries 42 migrations. A note in [`picoclaw-do-server-deploy`] says prod
never used `prisma migrate`. If `_prisma_migrations` is absent or sparse on prod, that
first boot will try to create tables that already exist, fail partway, and record a
failed migration — which is P3009, and P3009 crash-loops the API on every subsequent
start. That exact failure took the dev box down on 2026-08-14.

**Settle this before anything else.** If prod is not Prisma-managed, the migrations must
be baselined (`prisma migrate resolve --applied` for everything already present) in a
maintenance window, with a database snapshot taken first — not discovered during a
deploy.

Of the five migrations this release adds, one is destructive:

| migration | effect |
|---|---|
| `20260814000000_question_attempt_log` | new table, additive |
| `20260814010000_question_teach_text_and_distractors` | new columns, additive |
| `20260814020000_collapse_age_band_to_all` | rewrites `age_band` on active rows; guarded, no-op if the column is gone |
| `20260814030000_drop_age_band` | **DROPS `age_band`** from `quiz_question` and `riddle_question` |
| `20260814040000_kid_wonder_question` | new table, additive |

Reverting the merge does not put that column back. The database needs its own restore
plan, independent of git.

**per-age-banks/007 is superseded and must not be run.** It expands into per-age banks;
this release collapses them. Running it first would clone rows that `020000` then rewrites
and `030000` drops.

---

## Order

Load-bearing. Each step's failure mode is the next step's silent bug.

### 1. Snapshot

- Manual snapshot of the DigitalOcean managed Postgres (`db-postgresql-blr1-93302`).
  This is the only real rollback for the `age_band` drop.
- `dump-agent-prompt.js` into a dated folder — this is the fastest kill switch later.
- Note the currently running EKS image digest:
  `kubectl -n picoclaw-dev get deploy picoclaw-livekit -o jsonpath='{..image}'`
  (the manifest currently pins `sha256:071e9bcd…`; confirm the live one matches before
  changing it).

### 2. Database

Baseline first if step 0 showed prod is not Prisma-managed. Then let the migrations apply
— either by starting the new manager-api, or deliberately with `migrate deploy` in a
window where you are watching.

Verify after: `age_band` gone from both bank tables, `question_attempt` and
`kid_wonder_question` present, `teach_text` and `distractors` columns present.

### 3. Content — the bank

Prod's bank is either absent or the old 3-bands × 3-levels shape. This release expects
**24 levels × 10 questions on one bank**.

```bash
SRC_DATABASE_URL="<DB1>" DATABASE_URL="<prod>" node scripts/copy-quiz-tables.js --yes
```

Replaces `quiz_question`, `quiz_question_answer`, `riddle_question`,
`riddle_question_answer` only. Users, devices, kids and `ai_agent_template` are untouched,
so it cannot clobber prod credentials. It refuses if source and destination are the same
database, and resets the id sequences afterwards.

**Read the warning on this one.** It replaces the answer tables. If prod children have
quiz history, copying DB1's over it destroys their progress and violates the standing
"never rewrite the answer log" rule. If prod has any answer rows, do **not** use this
script — export the question tables alone and import those.

Verify: `SELECT level, count(*) FROM quiz_question WHERE active GROUP BY level` → 24 rows
of 10.

### 4. manager-api — before the worker

The worker calls `/quiz/*`. Deploy the worker first and every session silently falls back
to free chat, with no error anyone notices.

Prod services (`manager-api`, `manager-web`, `mqtt-gateway`) run on 139.59.7.72 /
ota.cheekoai.in. The admin dashboard ships with manager-api — the age control is gone and
the panel now reports level stats.

The `mqtt-gateway` restart drops every connected device. Pick a quiet window.

### 5. Prompt cutover

**After** the bank is seeded and `/quiz/*` is live, or `{{QUIZ_QUESTIONS}}` renders "bank
unavailable" and Quizzy refuses to quiz.

Prod's prompt is a third copy that has never been diffed. If it is still the pre-bank
8,312-char version, the patch scripts (`patch-quiz-prompt-008/015/016.js`) will not find
their anchors — they are written against dev's text. In that case load DB1's prompt
wholesale rather than patching, exactly as quiz-bank/008 planned.

Target: DB1's `quiz_master`, `system_prompt` 13,835 chars.

**Worker and prompt must move together.** On a prompt without the 016 line, `unclear=`
never arrives and every turn counts as an attempt again — not a crash, just the old bug
back, silently.

### 6. Worker to EKS — last

Recipe verified 2026-08-03, `deploy/README.md`:

ECR login → `docker build -f Dockerfile.eks -t <repo>:<date-tag> .` → push → grab digest →
pin it in `deploy/k8s/livekit-deployment.yaml` → `kubectl apply --dry-run=server` +
`kubectl diff` (**must show only the image line**) → apply → `rollout status`.

Zero-downtime (`maxUnavailable=0`, 900s grace), ~2 min for 2 pods.

Verify the code actually shipped — the image has no `strings`, and a `strings` miss
returns a misleading 0:

```bash
docker run --entrypoint sh <img> -c "grep -ac 'Door 3 success reported as revealed' /usr/local/bin/picoclaw-livekit"
```

CVE baseline: 1 CRITICAL + 3 HIGH in the base image, identical to the previously running
image. Compare against the live digest's scan rather than blocking on the raw count.

---

## Verify

One session on a prod device with a kid profile.

- [ ] `level=N questions=10` in the manager log — ten, not "however many are left"
- [ ] A wrong answer escalates Door 1 → 2 → 3, then reveals and scores `revealed`
- [ ] `question_attempt` rows appear for the question
- [ ] The session closes on a Wonder Question, and `kid_wonder_question` holds **one** row
- [ ] The next session opens by recalling it and closes on a different one
- [ ] `revealed` does not clear — the level stays open
- [ ] The admin panel shows `level N/24 — c/10 cleared · day d/3`

## Rollback

| Layer | How | Speed |
|---|---|---|
| Prompt | re-load the step-1 snapshot | seconds, no deploy — **fastest kill switch** |
| Worker | `kubectl -n picoclaw-dev rollout undo deployment/picoclaw-livekit` | ~2 min |
| Services | redeploy `pre-quizzy-merge` | minutes |
| Schema | **database snapshot restore** — `git revert` does not restore `age_band` | slow, disruptive |

If the first session looks wrong, roll the prompt back before unwinding any deploy.

## Known gaps shipping with this release

- **210 of 240 questions have no `teach_text` or distractors.** Levels 4–24. Doors 2 and 3
  are skipped for all of them, so a child past level 3 gets plain-ask-then-reveal with no
  ladder. [014](014-relevel-both-banks.md) is the authoring ticket.
- **The `revealed` blast radius has never been measured on prod.** Zero on local and dev.
  Non-zero on prod means real children are mid-progress on questions that are about to
  reopen — and 002's "no grandfather clause" decision was made on dev data only.
- **Riddler is not re-levelled.** Still 3 × 80. Harmless — Riddler has no Doors and
  `clearOnReveal` keeps it on flow — but its bank shape no longer matches Quizzy's.
- **The Wonder Question opening repeats until a session completes.** An abandoned session
  produces no new one, so the next session recalls the same question. Correct by design,
  surprising in practice.
- **`anti_trap_advanced` fires on the first session at a new level only.** If you are
  watching logs for the advance, it appears once, not on every request.

[`cheeko-deploy-boundaries`]: agent may deploy only to the DO dev box 64.227.170.31
[`picoclaw-do-server-deploy`]: pm2 recipe and the `migrate deploy`-on-boot note
