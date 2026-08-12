---
status: closed
assignee: claude
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

- [x] The rolling summary is read from and written to the owner-keyed memory store,
      not `ai_agent.summary_memory`
- [x] Existing non-empty `summary_memory` values are migrated to a per-child
      document — under the existing key `summary`, not a new `conversation_summary`
      one (see Resolution)
- [x] Two children who have used the same toy have separate summaries, and neither
      appears in the other's session
- [x] A child moved to a new toy keeps their summary
- [x] `ai_agent.summary_memory` still exists in the schema and is no longer written
- [x] The existing size cap is respected — unchanged, the document write already
      went through `saveDeviceMemoryDocument`
- [ ] Verified from a live session — **deferred, no dev-box deploy**

## Blocked by

- `docs/issues/child-owned-state/003-memory-and-workspace-follow-the-child.md` — this
  moves the summary into the owner-keyed store, so that store has to be owner-keyed first


## Resolution

Shipped in `faeb4207` (cheeko-backend, `feat/child-owned-state`).

**Smaller than the ticket assumed, and one part bigger.** The ticket said to move the
summary into `device_memory_documents`. It was already there — `saveRollingOverallMemory`
has always written a `summary` document, and 003 made that owner-keyed. So the real
change is deleting the *duplicate* write to `ai_agent.summary_memory` and the read
fallback to it.

The ticket's `document_key = 'conversation_summary'` was not used. The existing key is
`summary`, already written, already read, already migrated by 003. Inventing a second
key would have orphaned every existing summary for no gain.

**The part that was bigger:** the matching read still queried `device_memory_documents`
by `mac_address` — a call site 003 missed, because it is a `findFirst` rather than one
of the compound-unique lookups the sweep targeted. Left alone, a child moving toys would
not have found their own summary, and 003 would have looked correct while being wrong.
Fixed here.

There is now **no fallback to the column**. A child with no summary starts with none,
rather than inheriting whichever child last used that character. That is the point.

`ai_agent.summary_memory` stays populated and still serves the character CRUD and the
admin dashboard, so reverting the code makes it authoritative again. The migration
seeds a per-child document from it first, guarded by `NOT EXISTS`, so nothing currently
stored is stranded and re-running it is a no-op.

Full suite: **1388 passed, 66 suites.** Three assertions that checked the `ai_agent`
write now assert the document write *and* that `ai_agent.update` is never called — the
behaviour they protected (rolling memory accumulates rather than replaces) is unchanged
and still covered. The now-unused `agentId` parameter was removed.
