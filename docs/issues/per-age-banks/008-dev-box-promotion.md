---
status: open
assignee: claude
---

# 008 — Dev box promotion

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

Run the migration on the DO dev box (`root@64.227.170.31`) so per-age banks are live
somewhere real, and 005's soak can start counting for [007](007-production-promotion.md).

Everything in 001–006 was done against the **local** Supabase project
`shlrfpbqkfnxqcmuatvs`:6543. The dev box's manager-api points at a **different**
database — `tsiocygczplmnjpqmutc`:5432, "DB1" — so none of the data work carries over.
The code is the same; the migration has to be run again.

### Survey, 2026-08-12 (read-only, done)

DB1 is structurally identical to local before the migration, which is the good case:

- Both banks: 3 bands × 3 levels × 10 active, `en` only. Constraints are the original
  three-value ones. Longest code 15 chars, so `-a10` stays far inside `VARCHAR(50)`.
- Answer log is bigger than local: 64 quiz answers over 5 devices, 10 riddle over 1.
- **Every one of those 74 answers remaps cleanly — zero dormant.** Four devices have no
  `birth_date` and default to band 6, which is inside the `6-8` band they played; the
  fifth has a real birth date (age 4) and played `3-5` content. Local stranded 10 riddle
  answers this way; DB1 strands none.

Two stale facts in `picoclaw-do-server-deploy` corrected while checking: the server repo
is on branch **`stt-ptt-batch`**, not `main`, and the riddle seed CSVs are present, so
the riddle bank was promoted here at some point.

### What ships

**picoclaw: nothing.** Its 9 changed files are all documentation — no Go, no rebuild, no
`pm2 restart picoclaw-livekit`. This is not an assumption: the 2026-08-12 live age-3
session ran on a binary predating the entire piece of work and carried `band=3` through
the prompt, the MEMO channel and the answer POST. `age_band` is opaque to the worker.

**cheeko-backend: 21 files**, all under `manager-api-node` and `admin-dashboard` —
the two migrations, `quiz.logic.js`, `quiz.service.js`, the importer, the verifier, the
seed CSVs (4 added/modified, 6 deleted), the dashboard routes and its two static files.

No `prisma generate`: `schema.prisma` is untouched and the constraint change is SQL-only.

### Order

Expand → deploy → soak → contract, for the same reason as locally: retiring the old
bands before the code lands leaves every child querying a band with no active rows and
falling through to free chat.

| # | Step | Rollback |
|---|---|---|
| 1 | Apply the **expand** migration + remap to DB1 | additive — old rows stay active, nothing reads the clones |
| 2 | `verify-per-age-banks.js` — the *retirement* check must FAIL here | n/a |
| 3 | Ship the 21 files, `node --check`, `pm2 restart manager-api` | `git checkout -- <files>` + restart |
| 4 | Verify over HTTP, then one live **Quizzy** and one live **Riddler** session | revert the deploy |
| 5 | Import the ages 3–5 content | re-import from `-all.csv` |
| 6 | **Soak — days, not the hour 005 got locally** | n/a |
| 7 | Apply the **contract** migration | none; one-way door |

Steps 1 and 3 cannot be reordered. Step 7 ends the easy rollback.

## Acceptance criteria

- [ ] Post-expand, every (age 3–10 × level × language) has exactly 10 active rows in both
      DB1 banks, and the old rows are still active
- [ ] All 74 pre-existing answers have per-age twins; none deleted
- [ ] `pm2 restart manager-api` comes back healthy and `GET /quiz/next-questions` returns
      a single-age `age_band` for a real dev-box device
- [ ] A live Quizzy session on the dev box logs answers against `-a<age>` codes
- [ ] **A live Riddler session** does the same, into `riddle_question_answer` only — the
      one thing never yet watched in any environment
- [ ] The admin dashboard's Test tab panel works against the dev box (`ADMIN_PASSWORD`
      is set there)
- [ ] Ages 3, 4 and 5 serve the authored content, not clones
- [ ] Post-contract, no old-band row is active, no superseded answer copies remain, and
      the admin `Correct` column matches answers actually given

## Blocked by

- `docs/issues/per-age-banks/005-retire-old-bands.md` — the full sequence is proven
  locally, which is what makes running it here a repeat rather than an experiment

## Notes

- Deploy boundary: dev box only. Production (`139.59.7.72`, EKS `picoclaw-dev`) is
  [007](007-production-promotion.md) and is Rahul's to run.
- The server tree is shipped to by tar-over-ssh rather than by pulling a branch, per
  `picoclaw-do-server-deploy`. It keeps the tree otherwise clean and makes
  `git checkout -- <file>` a real rollback, at the cost of leaving the box on a tree
  that matches no commit until the branch merges.
- The age-4 device (`14:c1:9f:d6:44:f4`) is the one real child here whose experience
  changes: it moves to band 4 and, after step 5, starts getting authored age-4 questions
  instead of the shared `3-5` set.
