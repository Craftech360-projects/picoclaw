# 006 — Quizzy prompt cutover + full E2E verification

**Type:** HITL · **Status:** closed 2026-08-05 — verified in live sessions; one criterion
carried forward to [007](007-api-down-free-chat-path.md)
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
- [x] Live session on test device `00:16:3e:ac:b5:38` (child "Kishore"): zero invented
      scored questions across every session on 2026-08-05; `quiz_question_answer` rows match;
      MEMOs carry the judged question and result
- [ ] **Carried forward to 007** — Failure path: with manager-api stopped, Quizzy offers free
      chat, invents no scored questions, writes no answer rows. Never exercised.
- [x] Next-day check: resume verified, though by backdating rows rather than a real calendar
      rollover (see below)
- [x] Deploy boundary respected: dev box only. Prod was surveyed read-only on 2026-08-05;
      nothing written. Promotion remains a separate, user-granted decision.

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

## Closed 2026-08-05

**The live session criterion passed.** Multiple real sessions on `00:16:3e:ac:b5:38` ran
band 6-8 level 2 (ids 11–20) and level 3 (ids 102–111). Every scored question came from the
bank — zero invented. Answer rows matched, and MEMOs carried the judged question and verdict.

One defect surfaced and is worth recording because it is NOT the drift this ticket guarded
against. Question 14 was answered **out of order** — the sequence logged was
`12,13,15,16,17,18,19,20,14`. Cause: the turn that judged q14 died mid-stream (22.6s to first
token, then a timeout with the fallback line), so no MEMO came back and no answer was posted.
Quizzy moved on, and because `next-questions` recomputes Cleared from the log, q14 stayed
uncleared and was re-served at the end. **The record ended correct** — the server being the
source of truth is what saved it. But the child heard an apology instead of a verdict, then
got the question again out of sequence. Root cause was LLM upstream latency, fixed separately
(`5421d49`); the durable fix for lost-verdict reordering is proposed but unbuilt.

**Field-name correction:** this ticket specifies MEMOs carrying `q=<id>`. The implemented
field is `scored_q=` (with `scored_text=`), because verdict attribution is to the question
just *judged*, not the one pending — reading the pending id logged every answer one question
late. Anything written against `q=` will not match.

**Next-day resume verified, with a caveat on method.** Rather than wait for a calendar
rollover, answer rows were backdated one day. `14:C1:9F:D6:44:F4` cleared 3-5 level 1 and was
correctly served level 2; Kishore cleared 6-8 level 2 and was served level 3. The day-gate was
observed closing at `answered_today=10 → day_complete=true` and re-opening once rows moved.
The mechanism is proven; the celebration / Bonus Buzz / refuse-second-run *dialogue* was not
observed live with a child.

**A bug this ticket's verification found.** `quiz_bank.md` was never being written for Quizzy
at all — `WriteQuizBankState` sat in the non-quiz branch after a refactor, and worse, a
Cheeko or Nani session on the same device actively deleted it. Fixed in `dccd90d`. This was
the real mechanism behind "question drift after summarization": the state file that exists to
survive compaction was absent.

**Prod readiness note (read-only survey, 2026-08-05).** Prod has no quiz tables and the
migration is unapplied; the `quiz_master` row exists but is **not** cut over. Of 55 devices,
only 10 have a kid profile — the other 45 would default to band 6-8 regardless of the child's
real age. Of the 10 profiled, **nine are band 3-5 and one is 6-8**, so the band with the most
live validation serves the fewest real children. Promotion should not proceed until the
unprofiled-device policy is decided and band 3-5 has end-to-end session coverage.
