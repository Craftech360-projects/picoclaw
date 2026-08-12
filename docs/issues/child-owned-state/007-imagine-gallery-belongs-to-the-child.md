---
status: open
assignee:
---

# 007 — Imagine gallery belongs to the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

Imagine is the only feature in this phase with **no database row at all**. An image
is uploaded to S3 under `imagine/<mac>/<uuid>.jpg` and the parent's feed is a
`ListObjectsV2` call on that prefix. There is nothing to re-key, so this slice adds
the missing table rather than migrating a column.

New `imagine_image` — `owner_key`, `mac_address`, `s3_key`, `created_at`. Written at
upload, when the gateway POSTs the JPEG with a `deviceMac` and the manager resolves
the child. The feed reads the table instead of listing the bucket.

**S3 keys never move.** That is the point of putting the attribution in a row: a
child changing toys is a row-level fact, and no object ever has to be copied.

Backfill by walking the existing `imagine/<mac>/` prefixes once and inserting a row
per object, attributed to that device's current child. For a device that has served
two children this merges their galleries — but the feed merges them today anyway, so
it is not a regression, and the survey found no device on DB1 has changed hands.
Preserve S3 `LastModified` as `created_at` so the ordering the parent already sees
does not change.

This also fixes a live bug: the listing has no continuation token, so the gallery is
silently capped at 1000 objects and the date filter is applied in memory to whatever
that first page contained. Reading from a table removes both limits.

Add a per-child feed endpoint alongside the existing per-device one. The device
endpoint stays and resolves through the device's current child, so the app needs no
release; the per-child route is what lets a parent with two children see two
galleries once a toy has been handed down.

## Acceptance criteria

- [ ] `imagine_image` exists and a row is written on every successful upload
- [ ] Backfill inserts one row per existing object with `created_at` from S3
      `LastModified`, and is safe to re-run without duplicating
- [ ] The parent feed reads the table; ordering matches what the S3 listing returned
- [ ] A gallery with more than 1000 images pages correctly, and the date filter is
      applied in the query rather than in memory
- [ ] A child moved to a new toy still sees images they made on the old one
- [ ] A sibling paired to a used toy sees none of the previous child's images
- [ ] A per-child feed endpoint returns exactly that child's images; the existing
      per-device endpoint still works unchanged for the app
- [ ] No S3 object is copied, moved or renamed by any part of this

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`
