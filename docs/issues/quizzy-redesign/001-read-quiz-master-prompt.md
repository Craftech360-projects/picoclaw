# 001 — Dump and read the `quiz_master` prompt

**Type:** HITL · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §12a, §14 action 1.

## What to build

No part of the Quizzy redesign is based on reading Quizzy's actual prompt. It is not
in this repo — it lives in the Manager DB, in `ai_agent_template`. Everything the GDD
says about current behaviour is inferred from ticket 006, ADR-0005 and the worker code
that consumes the prompt.

Dump the prompt, read it, and record what it actually says in a comment on this issue.
This is a read-only investigation. Nothing here changes the prompt — that happens in
008 and 010, each with its own backup.

```sql
SELECT agent_code, agent_name, length(system_prompt), length(greeting_prompt)
FROM ai_agent_template WHERE agent_code IN ('quiz_master','riddler');
```

**Trap:** the row is `agent_code = 'quiz_master'`, **not** `'quizzy'`. A query written
from the obvious guess matches zero rows and returns nothing silently — it already
produced an empty backup file once during ticket 006.

Four things to check in `system_prompt`, in priority order:

1. **What the two-tries-then-reveal flow actually says** — does it explain anything,
   or does it only state the answer? M2a (micro-teach) assumes the latter. If the
   prompt already teaches, M2a is smaller than specced, and issue 011 shrinks.
2. **Whether the day-gate / "refuse a second scored run" wording** survives the new
   mastery rule or now contradicts it.
3. **Which parts hardcode `revealed` semantics** in the MEMO instruction — these are
   the exact lines issue 008 must rewrite.
4. **Whether the multilingual judging rule** (added 2026-08-04) needs extending for
   the Doors. Door 3's guided phrasing is far looser than today's asks.

## Acceptance criteria

- [ ] Both rows located using `agent_code IN ('quiz_master','riddler')`; row count and prompt lengths recorded
- [ ] `system_prompt` for `quiz_master` dumped to a backup file, and the file confirmed non-empty before proceeding
- [ ] Each of the four checks above answered explicitly in a comment on this issue, quoting the relevant prompt lines
- [ ] Any GDD claim contradicted by the real prompt is listed, so the affected design doc can be corrected
- [ ] Stated explicitly whether M2a is still needed as specced (drives scope of 011)
- [ ] No `UPDATE` executed under this issue

## Blocked by

None - can start immediately. Needs Manager DB access (`D:\cheeko-backend\main\manager-api-node`).
