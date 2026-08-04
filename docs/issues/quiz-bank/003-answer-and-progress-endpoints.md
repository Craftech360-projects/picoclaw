# 003 — Answer log + progress: POST /quiz/answer, GET /quiz/progress

**Type:** AFK · **Status:** closed
**Spec / Plan:** as 001 (plan Task 3, write/read half)
**Repo:** manager-api-node

## What to build

The write path and the parent-facing read path over the answer log:

- `POST /quiz/answer` body `{device_mac, question_id, result}` → inserts one `quiz_question_answer` row. `result` must be `correct|wrong|revealed`; unknown question or bad result → `400`. Called by the worker per question in real time.
- `GET /quiz/progress?device_mac=...` → `{age_band, current_level, levels_completed, counts: {correct, wrong, revealed}, last_played}` — read-only aggregate for a future parent dashboard; no UI in scope.

Same auth middleware as 002.

## Acceptance criteria

- [ ] `curl` an answer → row visible in `quiz_question_answer`; bad result / unknown id → `400` with message
- [ ] After answering a seeded question `correct`, 002's next-questions no longer returns it (Cleared by construction)
- [ ] Progress counts and `levels_completed` match hand-inserted fixtures; `current_level` is null when all levels cleared
- [ ] Committed on the manager repo branch

## Blocked by

- 002 (route file, service, auth pattern established there) — closed

## Resolution

Shipped in `fd65623e` (manager-api-node), extending the SUB-002 quiz modules in place.
25 pure-logic tests (18 existing + 7 new for `countCompletedLevels`); full suite 374
passing with only the known `prisma-client-guard` failure.

Round-trip verified live against dev DB2 with a throwaway MAC: answering a question
`correct` removes it from the next batch; `revealed` also clears it; `wrong` does not —
exactly the Cleared rule. Progress tracked correctly through clearing level 1
(`current_level` 1→2, `levels_completed` 1) and then level 2 (`current_level` null,
`levels_completed` 2), at which point next-questions flipped to champion replay. All six
error paths return 400 with a clear message; no service key returns 401. All 21 test
rows deleted afterwards — answer table back to 0, seed questions untouched at 20.

**Judgement call worth revisiting for the parent dashboard:** `counts` are lifetime
totals across all bands (so a birthday doesn't erase history), while `current_level` and
`levels_completed` are band-scoped. The ticket didn't pin this down.

Not added: HTTP-level regression tests. Endpoint behaviour is covered by live curl only,
matching how 002 left things.
