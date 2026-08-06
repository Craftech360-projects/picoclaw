---
status: closed
assignee: claude
---

# 001 — Riddle bank reachable over HTTP

## Parent

`docs/issues/riddle-bank/000-design.md`

## What to build

A second question bank, stored in its own tables, reachable through the existing quiz
endpoints by naming a character. This slice cuts the whole server-side path: schema →
derive logic → HTTP response. No worker changes.

`quiz.service.js` currently hardcodes `prisma.quiz_question` and
`prisma.quiz_question_answer`. Replace those with a bank looked up per request. The bank
is chosen by the caller's `agent_code`, defaulting to `quiz` when the param is absent or
unrecognised — so today's callers, which send no param, are unaffected.

```js
// src/services/banks.js
const BANKS = {
  quiz:   { questions: prisma.quiz_question,   answers: prisma.quiz_question_answer   },
  riddle: { questions: prisma.riddle_question, answers: prisma.riddle_question_answer },
};
const CHARACTER_BANK = { quiz_master: 'quiz', riddle_master: 'riddle' };
const bankFor = (character) => CHARACTER_BANK[character] ?? 'quiz';
```

`riddle_question_answer` names its foreign key `question_id`, not `riddle_id`, so both
answer tables are column-identical and the shared service needs no field indirection.

`src/services/quiz.logic.js` is pure and table-agnostic. It is reused verbatim. **Do not
modify it** — its existing tests are what keep the derive behaviour trustworthy for both
banks.

Seed only band `6-8`, level 1 in this slice. The remaining content is 003.

Work against the local dev database configured in
`D:\cheeko-backend\main\manager-api-node\.env`.

## Acceptance criteria

- [x] Migration `add_riddle_question_bank` creates `riddle_question` (`code` unique,
      `question_text` — see Resolution, not `riddle_text` — `answer_text`,
      `accepted_answers` JSONB, `age_band`, `level`, `language`, `active`) and
      `riddle_question_answer` (`device_mac`, `question_id`, `result`, `answered_at`)
- [x] `prisma generate` run after the schema change, and `prisma.riddle_question` resolves
      (otherwise the importer fails on every row)
- [x] The datasource host printed by any Prisma command is confirmed to be the intended dev
      database — `prisma.config.ts` overrides `DATABASE_URL` and has silently targeted the
      wrong host before
- [x] `src/services/banks.js` exists with `BANKS`, `CHARACTER_BANK` and `bankFor`
- [x] Every `quiz.service.js` function takes a bank and defaults to `quiz`
- [x] `GET /quiz/next-questions`, `POST /quiz/answer` and `GET /quiz/progress` accept an
      optional `character` param and route accordingly
- [x] `POST /quiz/answer` accepts an optional `bank` field and routes accordingly
- [x] Importer accepts `--bank riddle`, upserts by `code`, is idempotent, and exits non-zero
      if a seeded level has ≠ 10 active rows
- [x] `riddle-bank-6-8.csv` seeded with 10 level-1 riddles; `accepted_answers` is
      pipe-separated (commas are rejected)
- [x] `quiz.logic.js` is byte-identical to its state before this slice
- [x] Every pre-existing quiz test passes with no changes to the test files
- [x] New test: an answer recorded against the riddle bank appears in
      `riddle_question_answer` and not in `quiz_question_answer`
- [x] New test: `bankFor` maps `riddle_master` → riddle, and falls back to quiz for
      `quiz_master`, `undefined` and an unknown code
- [x] `curl` of `/quiz/next-questions` with and without `character=riddle_master` returns
      different question sets for the same device MAC

## Blocked by

None - can start immediately.

## Resolution

Shipped as `4251f7d0` in `manager-api-node` (branch `feat/riddle-bank`). All 14
acceptance criteria verified, one with a deliberate deviation noted below.

**Deviation — `question_text`, not `riddle_text`.** The ticket named the column
`riddle_text`. It shipped as `question_text`, for the same reason the ticket already
gave for naming the foreign key `question_id`: column-identical tables let one service
query either bank with no field mapping. `riddle_text` would have put a per-bank field
name into every query, the importer, `toQuestion()` and the admin page, to buy a better
noun. Recorded in `banks.js` and in the migration comment.

**Two problems the ticket did not anticipate, both fixed here:**

1. `kid_learning_progress` is unique on `(kid_id, subject, topic)` and topic is only
   `"<band> level <n>"`. Sharing the subject `'quiz'` across banks would make finishing
   riddle level 1 overwrite the child's quiz level 1 achievement. Each bank now carries
   its own `subject`.
2. `clearDayGate` backdates rows with raw SQL, and a tagged template cannot bind a table
   name. It now uses `$executeRawUnsafe` with the table name interpolated from the bank
   registry and both values still bound.

**Also shipped beyond the listed criteria:** the three admin endpoints take an optional
`bank` param (defaulting to quiz), which ticket 004 assumed would exist after this slice.
`next-questions`, `answer` and `progress` all echo `bank` in their response so the worker
never needs the character-to-bank mapping.

**Found in review and fixed before commit:** a bare `--bank` with no value silently
imported into the quiz bank; `bankFor` resolved inherited properties (`constructor`,
`toString`) off a plain object from a query string; and two seeded riddles shared the
answer "a clock", which would have failed 003's no-duplicate-answers criterion.

**Verified live** against the dev Supabase database: the same 6-8 device returns 10 quiz
questions with no `character` and 10 riddles with `character=riddle_master`; ids overlap
across banks (both start at 1), which is why `recordAnswer` looks the question up in the
claimed bank. A riddle answer wrote one row to `riddle_question_answer` and none to
`quiz_question_answer`; an absent bank defaulted to quiz; an unknown bank returned 400.
Test rows were on a synthetic MAC and were removed.

**Startup guard (`32282b52`, added after review at the user's request).** Both banks'
tables — quiz and riddle — are now in `REQUIRED_PRISMA_MODELS` in
`src/config/prisma-client-guard.js`. A deploy that ships the code without the migration
now fails at boot with a clear message instead of serving every child an empty bank and
looking like a content problem.

Consequence for 005: the schema-before-code deploy order is now **mandatory**, not merely
recommended. Applying the migration first is no longer a nicety — the API will not start
without it.

The guard's test fixture was hand-listed and had already rotted (the suite was failing on
`pending_card_pairing` before this work). It is now built from `REQUIRED_PRISMA_MODELS`,
so it cannot drift again. That suite passes for the first time in this branch.

Note: on the dev box `SKIP_DB_SYNC=1` skips the database-table half of the guard at boot,
so only the delegate half runs there. Both halves were verified explicitly against the dev
database by calling them directly.

**Test evidence:** 86 passing across the four quiz/riddle unit suites; `quiz.logic.js`
byte-identical and its tests unchanged. Three suites fail on a clean tree too
(`prisma-client-guard`, `imagine`, `rate-limit-logging`) and are unrelated —
`prisma-client-guard` fails because its fixture omits `pending_card_pairing`.
`config` and `ota` time out only inside the full 60-suite run and pass standalone in 9s
with these changes applied.
