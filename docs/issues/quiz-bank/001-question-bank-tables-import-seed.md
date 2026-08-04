# 001 — Question Bank tables + xlsx import + seed content

**Type:** HITL · **Status:** ready
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

- [ ] Migration applied via `prisma migrate` (never hand-created tables) to both dev DBs — **ask the user** which DB `.env` targets and for DB1 credentials
- [ ] `npx prisma migrate status` clean; `prisma validate` passes
- [ ] Import script run twice on a fixture reports identical counts (idempotent); bad band / missing answer rows are skipped with row numbers
- [ ] 20 seed questions for band 6-8 (codes `6-8-L01-Q01`…`6-8-L02-Q10`) **signed off by the user** and present in dev DB1
- [ ] Committed on the manager repo branch

## Blocked by

None — can start immediately (HITL gates: DB target/credentials, seed sign-off).
