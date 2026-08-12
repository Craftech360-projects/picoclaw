---
status: open
assignee:
---

# 006 — Chat history attributes to the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

`voice_sessions` already has a `kid_id` column and an index on
`(kid_id, started_at desc)`. The survey found **451 sessions with it null** — it is
declared, indexed, and largely unpopulated. Individual `voice_session_messages` rows
have no child reference at all and are reachable only through the session join, which
is correct and should stay that way: the message belongs to the session, the session
belongs to the child.

Populate `kid_id` on every session upsert, backfill the existing rows from
`ai_device`, and switch the history read paths to resolve by child, with the same
null-guarded MAC fallback used in 002.

There is no separate transcript table to migrate. `ai_agent_chat_history` is the
legacy xiaozhi table — zero rows and zero lifetime inserts on DB1, no writer in the
codebase, one residual reader that only uses it as a boolean personalisation signal.
It is removed in 009, not here.

The parent app's session list and the founder dashboard both read this data. Their
results should not change for a device that has only ever had one child, which is
every device on DB1 today — that is the regression test.

## Acceptance criteria

- [ ] `voice_sessions.kid_id` is written on session upsert, from the device row
- [ ] Backfill leaves zero sessions with a null `kid_id` for a paired device
- [ ] Chat history and session lists resolve by child, falling back to MAC only for
      unpaired devices and only for rows with no child
- [ ] A child moved to a new toy sees their earlier conversations
- [ ] A sibling paired to a used toy sees none of the previous child's conversations
- [ ] The parent app session list and the founder dashboard return identical results
      to today for a device that has only had one child
- [ ] Message rows are still reached through the session, not given their own child
      column

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`
