# 006 — Quizzy prompt cutover + full E2E verification

**Type:** HITL · **Status:** open — deployed and cut over; awaiting live-session verification
**Spec / Plan:** as 001 (plan Tasks 8–9)
**Repo:** none (dev DB `ai_agent_template` row + dev box verification)

## What to build

Cut the live Quizzy character over from LLM-invented questions to the bank, and prove the whole design with real session data (never by reasoning — pull transcripts and rows).

Prompt changes on the `quizzy` row (backup first, explicit column lists, show the user the diff before UPDATE):

- Insert `{{QUIZ_QUESTIONS}}`; replace all "invent a question" language with "ask the next question from the list in your own warm words; judge against the given answer/accepted alternates".
- Two wrong tries → reveal the answer warmly, emit `result=revealed` (nothing blocks progression).
- MEMO instruction extended with `q=<id> | result=...` on every scored judgment.
- Champion-rounds framing rule; day-gate / "refuse a second scored run" text kept; `{{TODAY_PLAN}}` rotation removed from Quizzy's prompts only.
- Bonus Buzz stays LLM-invented (unscored, not logged) — deliberate, per ADR-0005.

## Acceptance criteria

- [x] Prompt backup exists on the dev box (`/root/quizzy_prompts_20260804.bak`, 15970 bytes);
      **user approved the diff** 2026-08-04 before the UPDATE ran
- [ ] Live session on test device `00:16:3e:ac:b5:38` (child "Kishore"): transcript in `voice_session_messages` shows only seeded questions — zero invented scored questions; `quiz_question_answer` rows match the child's actual answers; `memory/state/daily_quiz.md` MEMOs carry `q=`/`result=`
- [ ] Failure path: with manager-api stopped, Quizzy offers free chat, invents no scored questions, writes no answer rows
- [ ] Next-day check: partial level resumes with only uncleared questions; completing all ten → celebration, Bonus Buzz, second scored run refused
- [ ] Deploy boundary respected: dev box only; prod promotion is a separate, user-granted decision

## Blocked by

- 001 (seed), 004 (injection), 005 (reporting) — all closed

## Progress 2026-08-04 — deployed and cut over

**Deployed to the dev box** (`64.227.170.31`, dev only — prod untouched):
- `manager-api` and `picoclaw-livekit` both moved from their previous branches to
  `feat/quizzy-question-bank`. Both were verified as clean fast-forwards first (zero
  commits on the deployed branches were missing from mine), so no other work rolled back.
- Worker rebuilt on the server with `CGO_LDFLAGS='-lc++ -lc++abi'` (Windows cross-compile
  can't do opus). Registered clean as `cheeko-agent`; no new panics after restart.
- `GET /toy/quiz/next-questions?device_mac=00:16:3e:ac:b5:38` returns Kishore's level-1
  batch from DB1 over real HTTP, `age_band_defaulted: false` (his profile age resolved).

**Prompt cutover applied to DB1**, `system_prompt` 8303→9442 chars, `greeting_prompt`
1894→1807. Verified through the worker's own persona-pull endpoint (not just the DB):
`{{QUIZ_QUESTIONS}}` present, `{{TODAY_PLAN}}` gone, `q=QUESTION_ID` present, no
banana/moo seeds.

**Two traps caught before they caused damage:**
1. This ticket said `agent_code = 'quizzy'`. The real row is **`quiz_master`** (agent_name
   `quizzy`). The first backup returned `COPY 0` — an empty file — and an UPDATE written
   from this ticket would have matched zero rows and silently changed nothing.
2. Section 5 of the old system prompt handed the model example questions to imitate:
   *"Which animal says moo?"*, *"What colour is a banana?"*, *"How many fingers are on one
   hand?"* — the exact canonical repeats that motivated ADR-0005. The prompt was seeding
   the very problem the bank exists to fix. All three example blocks are now replaced with
   delivery guidance that names no questions.

**Not changed, deliberately:** the two-attempts-then-reveal flow in §3 already matched the
design and only gained MEMO reporting; the day-gate, safety rules, expression tags, word
limits, identity rules and the `soul` column are untouched.

**Multilingual answers (added 2026-08-04 after the cutover).** The old judging rule
accepted only *"natural Hindi or Hinglish equivalents"*, so a child answering "pathu"
(Malayalam) or "hathu" (Kannada) for ten could be marked wrong despite being right. Fixed
in the prompt, not the data: the bank cannot enumerate ten-plus languages per question,
but the model already knows the translations and only needed permission. Section 3 now
names the major Indian languages with the ten-example, and section 4 adds "judge the
meaning, not the language ... the listed alternatives are examples, not the complete set",
while keeping Quizzy's own speech in the session language. `system_prompt` 9442→9923.

**Caveat a prompt cannot fix:** STT runs before the judge. With the session language set
to English, Sarvam may garble "pathu" before the model ever sees it. Check the raw
transcript in `voice_session_messages` during the live test to separate an STT failure
from a judging failure.

**Known consequence:** with that flow every question ends `correct` or `revealed`, so
`result=wrong` will rarely be emitted and `counts.wrong` on the progress endpoint stays
near zero. Harmless; the value exists for a future flow that logs interim attempts.

**Remaining — needs a human at the device:** the live session, the API-down free-chat
check, and tomorrow's resume check. Rollback if needed: restore from
`/root/quizzy_prompts_20260804.bak` (CSV, explicit columns).
