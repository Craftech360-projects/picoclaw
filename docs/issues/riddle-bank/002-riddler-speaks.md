# 002 — Riddler speaks its riddles

## Parent

`docs/issues/riddle-bank/000-design.md`

## What to build

Make the worker able to serve a bank other than the quiz bank, then create the Riddler
character that uses it. This is the demoable slice: after it, a real device asks riddles.

The worker never learns what a riddle is. It passes the character's `agent_code` on the
fetch, and echoes back whichever bank the API says the batch came from.

```
GET /quiz/next-questions?device_mac=X&character=riddle_master  -> batch + "bank":"riddle"
POST /quiz/answer { device_mac, question_id, result, bank: "riddle" }
```

Three touch points in Go:

1. `pkg/livekit/quiz_bank.go` — recognise `{{RIDDLES}}` as an alias of
   `{{QUIZ_QUESTIONS}}`; `FetchQuizBatch` takes the character and appends `&character=`;
   `QuizBatch` gains a `Bank` field.
2. `pkg/livekit/quiz_state.go` — the answer reporter sends `bank` on the POST.
3. `cmd/picoclaw-livekit/main.go` — pass the character's `agent_code` into
   `FetchQuizBatch` at the speculative-fetch site (around line 615). **Confirm what the
   variable there actually holds before wiring it** — `agent_code` is the join key
   everywhere else, and a value like `quizzy` instead of `quiz_master` matches zero rows
   and fails silently.

The fetch stays speculative and must still be cancelled when the resolved prompt contains
no placeholder.

Nothing else in the worker changes. `RenderQuizQuestions`, `WriteQuizBankState`,
`memory/state/quiz_bank.md`, the `MEMO: type=daily_quiz` format, `scored_q=` attribution
and both verdict guards are reused as-is. A session runs exactly one character, so the
shared filenames cannot collide. **Do not rename them** — commit `dccd90d` was a bug where
the bank state write sat in the wrong branch and a second character's session deleted the
file.

The Riddler character is a new `ai_agent_template` row: `agent_code = 'riddle_master'`,
with `{{RIDDLES}}` in its `greeting_prompt`. Copy Quizzy's near-homophone judging rule
verbatim — riddle answers are looser than quiz answers and the STT confusions that cost
three turns on "heart" will hurt more here.

## Acceptance criteria

- [ ] `{{RIDDLES}}` and `{{QUIZ_QUESTIONS}}` render identically; a prompt containing
      neither is returned untouched
- [ ] `FetchQuizBatch` sends the character; `QuizBatch.Bank` is populated from the response
- [ ] The answer POST includes `bank`
- [ ] The value passed from `main.go` is verified to be `agent_code`, not the display name
- [ ] A character whose prompt has no placeholder still cancels the speculative fetch
- [ ] Go test: `{{RIDDLES}}` renders identically to `{{QUIZ_QUESTIONS}}`
- [ ] Go test: `bank` round-trips from the fetch response into the answer POST body
- [ ] All existing quiz Go tests pass unchanged
- [ ] `ai_agent_template` row created with `agent_code = 'riddle_master'` and a prompt
      using `{{RIDDLES}}`
- [ ] Live local session: Riddler asks its 10 riddles in bank order
- [ ] Rows land in `riddle_question_answer`; `quiz_question_answer` gains none
- [ ] A Quizzy session on the same device MAC still works and still writes to
      `quiz_question_answer`
- [ ] Verified from `voice_session_messages` and the DB rows, not from reading the code

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`
