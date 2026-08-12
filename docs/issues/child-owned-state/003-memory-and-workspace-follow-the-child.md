---
status: open
assignee:
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

- [ ] All three tables have a non-null `owner_key` and a unique index over
      `(owner_key, <path|document_key|content_hash>)`
- [ ] Backfill leaves no null `owner_key` and no row unattributed on DB1, including
      MACs with no `ai_device` row (they become `mac:`)
- [ ] Every read and every upsert goes through the shared helper — no call site
      constructs the key inline
- [ ] A child moved from device A to device B gets their `USER.md`, `MEMORY.md` and
      `memory/state/` files on the first session on B, and exactly one row per path
- [ ] A sibling paired to a previously-used device gets a clean workspace and can
      read none of the previous child's documents
- [ ] Running two sequential sessions on the same device produces no duplicate rows
      for any path
- [ ] The old `(mac_address, …)` unique indexes still exist and still hold
- [ ] Verified from the DB after a live session, not from the diff

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — the key
  resolves to `mac:` for every unpaired device, so backfilling before pairing is
  fixed puts 18 of 24 devices in the wrong namespace and needs re-running
