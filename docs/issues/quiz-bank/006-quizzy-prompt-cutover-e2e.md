# 006 — Quizzy prompt cutover + full E2E verification

**Type:** HITL · **Status:** ready
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

- [ ] Prompt backup exists on the dev box; **user approved the diff** before the UPDATE ran
- [ ] Live session on test device `00:16:3e:ac:b5:38` (child "Kishore"): transcript in `voice_session_messages` shows only seeded questions — zero invented scored questions; `quiz_question_answer` rows match the child's actual answers; `memory/state/daily_quiz.md` MEMOs carry `q=`/`result=`
- [ ] Failure path: with manager-api stopped, Quizzy offers free chat, invents no scored questions, writes no answer rows
- [ ] Next-day check: partial level resumes with only uncleared questions; completing all ten → celebration, Bonus Buzz, second scored run refused
- [ ] Deploy boundary respected: dev box only; prod promotion is a separate, user-granted decision

## Blocked by

- 001 (seed), 004 (injection), 005 (reporting)
