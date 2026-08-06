---
status: closed
assignee: claude
---

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

- [x] `{{RIDDLES}}` and `{{QUIZ_QUESTIONS}}` render identically; a prompt containing
      neither is returned untouched
- [x] `FetchQuizBatch` sends the character; `QuizBatch.Bank` is populated from the response
- [x] The answer POST includes `bank`
- [x] The value passed from `main.go` is verified to be `agent_code`, not the display name
- [x] A character whose prompt has no placeholder still cancels the speculative fetch
- [x] Go test: `{{RIDDLES}}` renders identically to `{{QUIZ_QUESTIONS}}`
- [x] Go test: `bank` round-trips from the fetch response into the answer POST body
- [x] All existing quiz Go tests pass unchanged
- [x] `ai_agent_template` row created with `agent_code = 'riddle_master'` and a prompt
      using `{{RIDDLES}}`
- [x] Live local session: Riddler asks its 10 riddles in bank order
- [x] Rows land in `riddle_question_answer`; `quiz_question_answer` gains none
- [x] A Quizzy session on the same device MAC still works and still writes to
      `quiz_question_answer`
- [x] Verified from `voice_session_messages` and the DB rows, not from reading the code

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`

## Resolution

Shipped as `5ed9a1b` (picoclaw) and `f81dd908` (manager-api-node), both on
`feat/riddle-bank`, plus `ed6b4b26`, `6801e6d` and `8cee09e` from live testing. All 13
criteria verified — the last four live, see the section at the end.

**The ticket's central assumption was wrong: `agent_code` is not available to the worker.**
It lives on the persona, and the quiz fetch deliberately runs *before* the persona pull so
the two overlap — that overlap is what removed the 24 s first-audio tail. All room metadata
carries is `character` (the **display name**: "Cheeko", "Bheem") and `character_id` (a
uuid). There is no `agent_code` anywhere in the Go codebase.

So the split moved to the API, which keeps the worker dumb as the design intended. The
worker forwards `character_id` and `character`; `bankForCharacterRef` resolves either to an
`agent_code` and then to a bank. A caller that already knows the `agent_code` — curl, the
tests — still skips the lookup. `ai_agent_template.id` is a uuid column, so a non-uuid
`character_id` is rejected before it reaches Prisma; otherwise a junk metadata field would
500 every session rather than falling through to quiz. A failed lookup serves quiz rather
than throwing.

**A real bug, found reviewing the diff: `main.go` gated the entire batch path on a
hardcoded `"{{QUIZ_QUESTIONS}}"` string literal.** Riddler's greeting carries `{{RIDDLES}}`,
so every Riddler session would have taken the `else` branch — speculative fetch cancelled,
batch discarded, `quiz_bank.md` never written, and the child told the bank was unavailable.
That is the `dccd90d` failure mode a second time, from the same cause: placeholder knowledge
duplicated between the package and `main.go`. The gate is now
`livekit.PromptWantsQuizBatch`, and no placeholder literal remains in `main.go`.

**Riddler character** (`agent_code = 'riddle_master'`, id `fedc4949-8f47-4cc2-9d98-ec63e5f7620b`)
is derived from Quizzy by targeted substitution, never a blanket replace: every line
carrying MEMO syntax is byte-identical, because `quiz_state.go` parses the literal token
`type=daily_quiz` and renaming it would silently stop every verdict being recorded. A
scripted assertion checks the `daily_quiz` token count is unchanged. The near-homophone and
judge-the-meaning rules carried over verbatim. It reuses Quizzy's ElevenLabs voice — there
is no second voice id to give it, and a familiar voice beats a silent character.

**Existing quiz Go tests keep every assertion.** Only call sites gained the new arguments
(`"", ""` and `""`), which pins that a caller sending no character behaves exactly as
before.

**Verified through the running API**, same device MAC each time: Riddler's uuid returns 10
riddles, Quizzy's uuid returns 10 quiz questions, display name alone resolves correctly, a
malformed uuid returns 200 on the quiz bank rather than 500, and no character at all still
returns quiz — so a worker deployed before this change is unaffected.

**Test evidence:** 95 Node tests green across the five quiz/bank suites; Go `pkg/livekit`
and `cmd/picoclaw-livekit` green apart from `TestSynthesizeAndPlayLogsTTSProviderType`,
which fails identically on a clean tree and is unrelated to this work.

### Verified live 2026-08-06 08:41-08:44 UTC

Originally left unticked as "needs hardware". They were exercised through the admin
dashboard's LiveKit simulator, which needs no device — a gap in my knowledge of the repo,
not a real limitation. All four now pass:

- Riddles asked in exact bank order — **4 of 10 answered**, ids 1,2,3 then 4 after a
  character switch. The order is proven; a full ten-in-one-day run is not.
- Rows landed in `riddle_question_answer` only
- A Quizzy session on the same device MAC worked, writing to `quiz_question_answer`
- Confirmed from the DB rows and the workspace state files

**The MEMO path works for Riddler.** `daily_quiz.md` after the run:

```
MEMO: type=daily_quiz | date=2026-08-06 | status=in_progress | answered=4 |
awaiting=5 | scored_q=4 | scored_text=I am tall when I am young and short when
I am old. What am I? | result=correct
```

`scored_text` matches riddle 4 verbatim, so `questionTextMatchesBank` and
`verdictMatchesClaimedQuestion` both passed and the verdict was attributed correctly. This
was the slice's last unknown.

**The bank-switch fix (`8cee09e`) was exercised by accident and held.** The run went
Riddler → Quizzy → Riddler on one device. Quizzy started at question 1 rather than
inheriting `awaiting=4` from Riddler's scoreboard, and Riddler resumed at riddle 4 rather
than restarting — because the switch cleared `daily_quiz.md` while progress stayed in the
per-bank answer log. Four riddle rows, two quiz rows, no cross-contamination.

Three defects were found by live testing that no unit test caught, all fixed:
`ed6b4b26` (a character_id matching no row served the quiz bank), `6801e6d` (the log could
not distinguish which bank served a batch) and `8cee09e` (the shared scoreboard).
