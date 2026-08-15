---
status: closed
assignee: claude
---

# 003 — Memory and workspace follow the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

`USER.md`, `MEMORY.md`, the quiz state ledgers under `memory/state/` and every
durable memory document are stored against the MAC. This is the slice that fixes
"the kid switched device and lost everything".

These three tables get **`owner_key`**, not `kid_id`:
`device_workspace_artifacts`, `device_memory_documents`, `device_memory_chunks`.

```
owner_key = 'kid:123'            when the device is paired
owner_key = 'mac:aa:bb:cc:...'   when it is not
```

The reason it is a namespaced string rather than a nullable `kid_id` is that all
three carry compound uniques — `(mac_address, relative_path)`,
`(mac_address, document_key)`, `(mac_address, content_hash)`. Postgres treats NULLs
as distinct in a unique index, so `UNIQUE(kid_id, path)` would let an unpaired
device write ten `MEMORY.md` rows, and Prisma's compound-unique `where` cannot take
a null — every upsert site would need a read-then-branch instead of a one-line key
swap.

Each call site becomes:

```js
// before
where: { mac_address_relative_path: { mac_address: normalizedMac, relative_path: path } }
// after
where: { owner_key_relative_path:   { owner_key: ownerKey,        relative_path: path } }
```

`ownerKey` comes from one helper, called where the device row is **already** being
loaded at every site. Roughly twelve sites across `workspace.service.js` and
`agent.service.js`: four artifact upserts, two artifact reads, one artifact
`deleteMany`, the memory-document upsert, the chunk delete/recreate pair, and the
memory-document list.

Getting the key right is what makes this correct rather than nearly correct. If
`owner_key` is added but the unique stays on `mac_address`, then on a new toy the
first workspace sync inserts a *second* `MEMORY.md` — the child appears to have
their history and then it is quietly shadowed by the fresh row.

Keep `mac_address` and `kid_id` as denormalised audit columns, written on every
upsert, read by nothing. Leave the old `(mac_address, …)` uniques in place for this
release — they stay satisfiable and give a clean rollback. Dropping them is a
follow-up.

## Acceptance criteria

- [x] All three tables have a non-null `owner_key` and a unique index over
      `(owner_key, <path|document_key|content_hash>)`
- [x] Backfill written, including the `mac:` fallback for MACs with no `ai_device`
      row and a defensive dedupe before the unique index — **not run on DB1**, no deploy
- [x] Every read and every upsert goes through the shared helper — no call site
      constructs the key inline
- [x] A child moved from device A to device B resolves to the same `owner_key`
      (`owner-key.test.js`), so the first session on B reads their existing rows
- [x] A sibling on a previously-used device cannot reach the previous child's rows —
      asserted as a property of the key, not of a query filter
- [~] Running two sequential sessions produces no duplicate rows — guaranteed by the
      new unique index, **not** exercised by a test that runs two sessions
- [x] The old `(mac_address, …)` unique indexes still exist and still hold
- [ ] Verified from the DB after a live session — **deferred, no dev-box deploy**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — the key
  resolves to `mac:` for every unpaired device, so backfilling before pairing is
  fixed puts 18 of 24 devices in the wrong namespace and needs re-running

## Resolution

Shipped in `47ee481b` (cheeko-backend, `feat/child-owned-state`).

Fourteen call sites, not the twelve estimated — the ticket missed the manifest
`findUnique` in `getWorkspaceSync` and the one inside the sync transaction. All go
through `ownerKeyForDevice`, and the two read paths that never loaded a device row
(`listDeviceWorkspaceArtifacts`, `listDeviceMemoryDocuments`) now do, via a small
`resolveOwnerKey`.

One decision the ticket did not call: **`mac_address` is not rewritten on update.**
It stays as the first writer's. Repointing it at the current device could collide
with a row that device left behind while unpaired, and the old
`(mac_address, relative_path)` unique is deliberately kept this release so rollback
is a code revert rather than a restore. It is an audit column; the owner key is the
identity.

`owner-key.test.js` asserts the correctness argument directly rather than only
through the services: the same child on two different toys produces the same key,
three spellings of one MAC produce one key, and a `kid:` key can never equal a
`mac:` key. That last one is why the sibling leak is impossible by construction
rather than by a caller remembering a guard.

Full suite: **1388 passed, 66 suites, zero failures.**

Assertions that encoded the old compound key were rewritten, not deleted. Six tests
failed for a reason worth recording: the read paths now load a device row, and those
suites had never mocked one — the failure was missing test setup, not broken
behaviour.

**Deviation from the process:** unlike 001 and 002 this was implementation-first.
The mechanical part (a key swap at fourteen sites) had no interesting seam to drive
from a test; `owner-key.test.js` was written afterwards against the property that
actually matters. Worth knowing when reading the history.
