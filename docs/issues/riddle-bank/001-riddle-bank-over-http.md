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

- [ ] Migration `add_riddle_question_bank` creates `riddle_question` (`code` unique,
      `riddle_text`, `answer_text`, `accepted_answers` JSONB, `age_band`, `level`,
      `language`, `active`) and `riddle_question_answer` (`device_mac`, `question_id`,
      `result`, `answered_at`)
- [ ] `prisma generate` run after the schema change, and `prisma.riddle_question` resolves
      (otherwise the importer fails on every row)
- [ ] The datasource host printed by any Prisma command is confirmed to be the intended dev
      database — `prisma.config.ts` overrides `DATABASE_URL` and has silently targeted the
      wrong host before
- [ ] `src/services/banks.js` exists with `BANKS`, `CHARACTER_BANK` and `bankFor`
- [ ] Every `quiz.service.js` function takes a bank and defaults to `quiz`
- [ ] `GET /quiz/next-questions`, `POST /quiz/answer` and `GET /quiz/progress` accept an
      optional `character` param and route accordingly
- [ ] `POST /quiz/answer` accepts an optional `bank` field and routes accordingly
- [ ] Importer accepts `--bank riddle`, upserts by `code`, is idempotent, and exits non-zero
      if a seeded level has ≠ 10 active rows
- [ ] `riddle-bank-6-8.csv` seeded with 10 level-1 riddles; `accepted_answers` is
      pipe-separated (commas are rejected)
- [ ] `quiz.logic.js` is byte-identical to its state before this slice
- [ ] Every pre-existing quiz test passes with no changes to the test files
- [ ] New test: an answer recorded against the riddle bank appears in
      `riddle_question_answer` and not in `quiz_question_answer`
- [ ] New test: `bankFor` maps `riddle_master` → riddle, and falls back to quiz for
      `quiz_master`, `undefined` and an unknown code
- [ ] `curl` of `/quiz/next-questions` with and without `character=riddle_master` returns
      different question sets for the same device MAC

## Blocked by

None - can start immediately.
