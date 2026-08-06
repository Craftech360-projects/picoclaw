# Riddler — a second bank-driven character

Design, 2026-08-06. Spans `D:\picoclaw` (Go LiveKit worker) and
`D:\cheeko-backend\main\manager-api-node` (Node/Prisma) plus `manager-web` for admin.

Riddler asks curated riddles the way Quizzy asks curated questions. The game loop is
**identical** to Quizzy's — ask, child answers, model judges, next — so almost all of the
quiz-bank machinery is reused unchanged. What is new: a second pair of tables, a bank
router in the API, and 90 riddles.

Background reading, not repeated here: `docs/adr/0005-quizzy-scored-questions-come-from-a-curated-bank.md`,
`CONTEXT.md`, `docs/issues/quiz-bank/001-008`.

---

## 1. Decisions

| Decision | Choice | Why |
|---|---|---|
| Game loop | Identical to Quizzy | No hints, no attempt counts, no child-led mode. Ten a day, three levels, `correct`/`revealed` |
| Storage | Separate `riddle_*` tables | A riddle can never leak into Quizzy's level state — the queries point at different tables |
| Daily budget | Per bank | 10 quiz + 10 riddles = 20. Falls out of separate tables for free |
| Service code | **One service, table pair passed in** | Isolation is a property of the tables. Duplicating 554 lines buys nothing and creates a permanent sync obligation |
| Placeholder | **New `{{RIDDLES}}` alias**, `{{QUIZ_QUESTIONS}}` still works | Prompt reads correctly for whoever edits it in the admin UI later |

---

## 2. Architecture — Node owns the split, the worker stays nearly dumb

The worker never learns what a riddle is. It passes the character through and echoes back
whichever bank the API says it got.

```
GET /quiz/next-questions?device_mac=X&character=riddle_master
    -> bank router: agent_code -> 'riddle'
    -> batch (unchanged shape) + "bank": "riddle"

POST /quiz/answer { device_mac, question_id, result, bank: "riddle" }
    -> routes to riddle_question_answer
```

No `character` param, or an unknown one, resolves to the **quiz** bank. Quizzy keeps working
with no coordinated deploy, and the worker can ship before or after the API.

### Unchanged in the worker

`RenderQuizQuestions`, `WriteQuizBankState`, `memory/state/quiz_bank.md`, the
`MEMO: type=daily_quiz` format, `scored_q=` attribution, and both verdict guards
(`questionTextMatchesBank`, `verdictMatchesClaimedQuestion`). Riddler reuses all of it.

A session runs exactly one character, so the shared state filenames and the shared MEMO
`type=` cannot collide. **Do not rename them** — every rename is a chance to reintroduce the
`dccd90d` bug where the bank state file was written on the wrong branch.

### Changed in the worker

Three touch points, all small:

1. `quiz_bank.go` — `{{RIDDLES}}` recognised as an alias of `{{QUIZ_QUESTIONS}}`; `FetchQuizBatch`
   takes the character and appends `&character=`; `QuizBatch` gains a `Bank` field.
2. `quiz_state.go` — the answer reporter sends `bank` on the POST.
3. `main.go` — pass the character's **`agent_code`** into `FetchQuizBatch` at the
   speculative-fetch site (line ~615). Not the display name: `agent_code` is the join key
   everywhere else, and `characterName` there is the display name. Confirm which value that
   variable actually holds before wiring it.

The fetch stays speculative and still cancels at line ~724 when the resolved prompt has no
placeholder.

---

## 3. Data

Two tables mirroring the quiz pair, migration `add_riddle_question_bank`:

- `riddle_question` — `code` (unique, the upsert key), `riddle_text`, `answer_text`,
  `accepted_answers` JSONB, `age_band`, `level`, `language`, `active`
- `riddle_question_answer` — append-only: `device_mac`, `question_id`, `result`, `answered_at`

Derive-don't-store carries over whole. There is no riddle progress table:

| Concept | Derivation |
|---|---|
| Age band | `ageBandFromBirthDate(kid.birth_date)` — reused as-is |
| Cleared | an answer row with `result IN ('correct','revealed')` |
| Current level | `deriveLevelState` — lowest level with an uncleared riddle |
| Day gate | 10 answer rows since server midnight (UTC, so 05:30 IST) |

`src/services/quiz.logic.js` is 88 lines and **completely pure** — `deriveLevelState(questions,
clearedIds)` takes arrays and knows nothing about tables. It is reused verbatim for riddles.
**Do not modify it.**

---

## 4. The bank router

`quiz.service.js` currently hardcodes `prisma.quiz_question` / `prisma.quiz_question_answer`.
It gains a bank parameter instead:

```js
// src/services/banks.js — ponytail: a lookup table, not a second service
const BANKS = {
  quiz:   { questions: prisma.quiz_question,   answers: prisma.quiz_question_answer   },
  riddle: { questions: prisma.riddle_question, answers: prisma.riddle_question_answer },
};
const CHARACTER_BANK = { quiz_master: 'quiz', riddle_master: 'riddle' };
const bankFor = (character) => CHARACTER_BANK[character] ?? 'quiz';
```

Every service function takes `bank` as its first argument and defaults to `'quiz'`. Adding a
third bank later is two more lines.

`riddle_question_answer` names its foreign key **`question_id`**, not `riddle_id`, so the two
answer tables are column-identical and the shared service needs no field indirection. The name
is slightly off for a riddle; identical query code is worth more than the better noun.

---

## 5. Slices

One vertical slice each, mirroring `docs/issues/quiz-bank/`. Files land in
`docs/issues/riddle-bank/`.

| # | Slice | Verify |
|---|---|---|
| 1 | Migration + `riddle-bank-{3-5,6-8,9plus}.csv` + importer takes `--bank riddle` | `npm run import:riddle-bank` is idempotent; exits non-zero if any level has ≠ 10 active rows |
| 2 | `banks.js` + `quiz.service.js` parameterised + `character`/`bank` params on the three endpoints | Every existing quiz test still green with no changes; new tests prove no cross-bank leakage |
| 3 | Worker: `{{RIDDLES}}` alias, character on fetch, `bank` echoed on answer | Go tests; one `curl` per bank against the same device MAC |
| 4 | `ai_agent_template` row `agent_code='riddle_master'`, prompt using `{{RIDDLES}}` | Live dev session: 10 riddles in bank order, rows land in `riddle_question_answer`, none in `quiz_question_answer` |
| 5 | Admin: bank selector on `/quiz-progress` | Both banks visible per device; set-level and reset-day work per bank |

Slice 2 is the risky one — it edits a file serving production. It ships behind a default
(`bank='quiz'`) so behaviour is unchanged until slice 3 starts sending the param.

---

## 6. Content

90 riddles: 3 bands × 3 levels × 10. **Author band 3-5 first** — nine of ten profiled
production devices are 3-5, and it is the band Quizzy has never been played through end to end.

Riddle answers are looser than quiz answers, so `accepted_answers` carries more weight here.
Pipe-separated in the CSV; commas are rejected. Copy the near-homophone prompt rule added
2026-08-05 into the Riddler prompt verbatim — Sarvam returning "hurt"/"hard" for "heart" will
hurt riddles more than quizzes.

Same ceiling as Quizzy: three levels ≈ three days, then `replay=true` re-serves the
least-recently-played level.

---

## 7. Testing

- `quiz.logic.js` untouched; its existing unit tests already cover the derive logic for both banks.
- New Node tests: `bankFor` mapping, and that a riddle answer writes only to `riddle_question_answer`.
- New Go test: `{{RIDDLES}}` renders identically to `{{QUIZ_QUESTIONS}}`, and `bank` round-trips
  fetch → answer POST.
- Live verification, per the habit that has paid off all week: pull the transcript from
  `voice_session_messages` and the rows from the DB. Do not conclude from code review — the two
  biggest findings of the quiz-bank build were invisible in code and only showed up in logs.

---

## 8. Explicitly out of scope

- Hints, attempt counts, or any riddle-specific mechanic. The loop is Quizzy's.
- The open Quizzy items (007 API-down path, lost-verdict reordering, `tools=1` on prod). They
  affect Riddler identically, and fixing them is separate work.
- Renaming the shared state files or the MEMO `type=daily_quiz`.
- A parent-portal view for riddles beyond the existing admin page.

## 9. Known traps that apply here

Carried from the quiz-bank build; all still live:

- `agent_code` is the join key, not `agent_name`. Quizzy's is `quiz_master`; an UPDATE written
  against the display name matches zero rows and succeeds silently.
- Production never used `prisma migrate` — `_prisma_migrations` does not exist. Apply the
  migration SQL directly; `migrate deploy` would replay everything.
- Run `prisma generate` after the schema change or `prisma.riddle_question` is `undefined` and
  the importer fails on every row.
- `prisma.config.ts` overrides `DATABASE_URL` on the dev box. Confirm the printed datasource host.
- Plain `node` scripts do not load `.env`; `set -a && . ./.env && set +a` first.
- `log.Printf` goes to stderr — under pm2 that is `*-error.log`.
