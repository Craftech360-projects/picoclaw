# 001 — Question Bank tables + xlsx import + seed content

**Type:** HITL · **Status:** closed
**Spec:** `docs/superpowers/specs/2026-08-04-quizzy-question-bank-design.md` · **Plan:** `docs/superpowers/plans/2026-08-04-quizzy-question-bank.md` (Tasks 1, 4, 8-step-1) · **ADR:** `docs/adr/0005-quizzy-scored-questions-come-from-a-curated-bank.md`
**Repo:** manager-api-node (branch `feat/quizzy-question-bank`)

## What to build

The Question Bank storage layer, end to end: a real Prisma migration adding `quiz_question` (the bank — `code` unique authoring key, question/answer/accepted answers, category, Age Band `3-5|6-8|9+`, Level, language, active) and `quiz_question_answer` (the log — device MAC, question, `correct|wrong|revealed`, timestamp; the only progress state, per the spec's derived-state rule). Plus an xlsx import script that upserts by `code` (idempotent re-runs, invalid rows reported and skipped), and an initial seed: 2 Levels × 10 questions for band 6-8, human-approved.

Schema shape is decision-rich — from the spec:

```prisma
model quiz_question {
  code / question_text / answer_text / accepted_answers Json
  category? / age_band / level Int / language "en" / active
}
model quiz_question_answer {
  device_mac / question_id FK / result / answered_at
}
```

## Acceptance criteria

- [x] Migration applied via `prisma migrate` (never hand-created tables) to **both dev DBs** — DB2 (`shlrfpbqkfnxqcmuatvs`) and DB1 (`tsiocygczplmnjpqmutc`, what the dev box uses)
- [x] `npx prisma migrate status` clean; `prisma validate` passes
- [x] Import script run twice on a fixture reports identical counts (idempotent); bad band / missing answer rows are skipped with row numbers
- [x] 20 seed questions for band 6-8 (codes `6-8-L01-Q01`…`6-8-L02-Q10`) **signed off by the user** (2026-08-04) and present in both dev DBs
- [x] Committed on the manager repo branch

## Blocked by

None — can start immediately (HITL gates: DB target/credentials, seed sign-off).

## Resolution

Shipped in `00b3b99f` on `feat/quizzy-question-bank` (manager-api-node).

**What works:** `quiz_question` + `quiz_question_answer` exist on DB2 with CHECK
constraints on `age_band`, `level` and `result` (all three verified rejecting bad
inserts with SQLSTATE 23514) and an `ON DELETE RESTRICT` FK so deleting a question
cannot erase answer history. `npm run import:quiz-bank <sheet>` upserts by `code`:
20 seed rows, exactly 10 per Level, identical counts on re-run, exit 0. Bad rows are
skipped by spreadsheet row number with the offending column named, and the run exits
1. A Level holding fewer than ten *active* questions is an error, not a warning.

**Notable bugs found by exercising the criteria rather than reasoning:**
- Spreadsheets parse the band `6-8` as a date, and *which* date depends on author
  locale (en-IN → 6 August, en-US → 8 June). Without recovery every hyphenated band
  row is rejected. Both orientations are now handled; unambiguous because no band is
  another band reversed.
- `scripts/lib/` was swallowed by a Python `lib/` rule in the repo-root `.gitignore`,
  so the parser would not have been committed — the script and its tests would fail
  with MODULE_NOT_FOUND on a fresh clone.

**Deferred (for the consumer tickets, 002/003):** `accepted_answers` does not include
`answer_text`, and 6 of 20 rows have an empty list — the judge must match against both
fields and case-fold. Answer casing is inconsistent across the file by design.

**DB1 rollout (2026-08-04):** questions signed off by the user; migration deployed and
seed imported to DB1. Only this migration was pending there — the eight subscription
migrations in DB1's ledger have no local counterpart and were left untouched. Verified
on DB1: 10 questions per Level across two Levels, CHECK constraints enforcing (23514),
re-import idempotent, DB2 unaffected. Both dev DBs now hold the identical bank.
Prod (DigitalOcean) deliberately untouched.

**Pre-existing, not mine:** `tests/unit/prisma-client-guard.test.js` fails on a clean
tree (verified by stashing); commit `fafa9549` added `pending_card_pairing` to the
guard without updating the test fixture. Spawned as a separate task.
