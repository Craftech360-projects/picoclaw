---
status: proposed
assignee: unassigned
---

# 011 — device_memory_documents still carries the pre-owner_key unique

## Parent

`docs/issues/child-owned-state/000-design.md`

## The symptom

Every session on an affected device ends with this, and the summary `PUT` returns
400:

```
prisma:error Invalid `prisma.device_memory_documents.upsert()` invocation
  agent.service.js:1164
  Unique constraint failed on the fields: (`mac_address`, `document_key`)
[error]: Failed to consolidate device memory for ended session
```

Observed live 2026-08-13 on `00:16:3E:AC:B5:38` during a Nani session.

## Why

The table holds **two** unique constraints (`prisma/schema.prisma:579-580`):

```prisma
@@unique([mac_address, document_key], map: "uq_device_memory_documents_mac_key")
@@unique([owner_key,   document_key], map: "uq_device_memory_documents_owner_key")
```

`owner_key` was introduced to make memory follow the child (`kid:15`, falling back
to `mac:<addr>` when unpaired). The legacy MAC unique was never dropped, and the
two disagree about identity the moment a device's pairing changes.

The live case, exactly:

| row | mac_address | owner_key | document_key |
|---|---|---|---|
| existing | `00:16:3E:AC:B5:38` | `kid:14` | `summary` |
| device now | `00:16:3E:AC:B5:38` | *unpaired* → `mac:00:16:3e:ac:b5:38` | — |

The upsert looks up `(owner_key='mac:…', document_key='summary')`, finds nothing,
tries to create — and collides with the `kid:14` row on
`(mac_address, document_key)`. There is no path out: that device can never write
its `summary` document again.

**This is not rare.** A DB1 sweep found **89 rows** whose `owner_key` disagrees
with their device's current pairing. Any of them hits this on its next session.

## Consequence

Per-session rows are unaffected — `voice_session_summaries` saves fine, and the
log line `[VOICE-SESSION] Saved session summary record` appears immediately before
the failure. What breaks is the **consolidated rolling memory**, so:

- the child's long-term "what we've talked about" document stops updating
- every session ends with a 400 the worker logs as a failed summary persist
- the shared-continuity half of `chat-history-attribution/000`'s decision quietly
  stops working on exactly the devices that have been re-paired

## Fix

**Drop `uq_device_memory_documents_mac_key`.** `owner_key` is the identity now;
the MAC unique is the old one asserting a rule the design has replaced. Removing
it lets two children who have used the same toy each keep their own document,
which is the whole point of `owner_key`.

Rejected alternative: make the upsert also match on MAC and adopt the existing
row. That hands `kid:14`'s memory to whoever holds the toy next — the precise
cross-child leak `child-owned-state` exists to prevent.

⚠️ This is a migration, and `server.js` runs `runPrismaMigrations()` on boot, so
the file must not be in the tree until you intend it applied (see
`010-dev-box-promotion.md` and the deploy notes). Production is a separate apply.

## Acceptance criteria

- [ ] Migration drops the MAC unique; `(owner_key, document_key)` remains
- [ ] Two rows may exist for one MAC under different owner keys — asserted
- [ ] A device whose pairing changed writes its `summary` document successfully
- [ ] Re-run the 89-row sweep: no session ends with the constraint error
- [ ] A child moved to a new toy still reads their own document, and a sibling on
      the old toy does not
- [ ] Applied to DB1; prod apply recorded separately or explicitly deferred

## Found by

`docs/issues/chat-history-attribution/004-backfill-and-live-verification.md` —
surfaced by a live Nani session, not by any test.
