# 003 — Answer log + progress: POST /quiz/answer, GET /quiz/progress

**Type:** AFK · **Status:** ready
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

- 002 (route file, service, auth pattern established there)
