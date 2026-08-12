---
status: open
assignee:
---

# 004 — The rolling summary belongs to the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

`ai_agent.summary_memory` is a rolling conversation summary that survives across
sessions and is fed back into later ones. It lives on the **Character** row reached
through `ai_device.agent_id`, which means it is neither device-scoped nor
child-scoped — it is character-scoped, shared by whoever uses that toy. Hand the toy
to a sibling and Cheeko asks the younger child how their football match went.

Do not add `kid_id` to `ai_agent`. That row is the Character, and a column there
gives one summary slot per character rather than per child — two children on the same
toy would still overwrite each other.

Move it into `device_memory_documents` as a row with `document_key =
'conversation_summary'`. That store is already owner-keyed by 003, already size-capped,
already syncs to the worker, and already has a read path. No new table, no new
migration beyond a data move.

Reads at the session-summarisation path change source. Migrate existing
`summary_memory` content into a document for whichever child the device is currently
paired to, then stop writing the column. Leave the column in place this release so
rollback is a code revert rather than a restore.

## Acceptance criteria

- [ ] The rolling summary is read from and written to the owner-keyed memory store,
      not `ai_agent.summary_memory`
- [ ] Existing non-empty `summary_memory` values are migrated to a
      `conversation_summary` document for the device's current child
- [ ] Two children who have used the same toy have separate summaries, and neither
      appears in the other's session
- [ ] A child moved to a new toy keeps their summary
- [ ] `ai_agent.summary_memory` still exists in the schema and is no longer written
- [ ] The existing size cap on memory documents is respected — a long summary is
      truncated, not rejected mid-session
- [ ] Verified from a live session: the summary injected into the prompt belongs to
      the child who is talking

## Blocked by

- `docs/issues/child-owned-state/003-memory-and-workspace-follow-the-child.md` — this
  moves the summary into the owner-keyed store, so that store has to be owner-keyed first
