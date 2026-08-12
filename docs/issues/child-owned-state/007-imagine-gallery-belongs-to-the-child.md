---
status: closed
assignee: claude
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

- [x] `imagine_image` exists and a row is written on every successful upload
- [x] Backfill written — walks the bucket with a continuation token, takes
      `created_at` from S3 `LastModified`, idempotent on the unique `s3_key`.
      **Not run anywhere**
- [x] The parent feed reads the table
- [x] Paging is by cursor with no ceiling, and the date filter is an indexed range
- [x] A child moved to a new toy still sees images they made on the old one
- [x] A sibling paired to a used toy sees none of the previous child's images
- [x] A per-child feed endpoint returns exactly that child's images; the per-device
      endpoint is unchanged in shape, so the app needs no release
- [x] No S3 object is copied, moved or renamed by any part of this
- [ ] Verified against real S3 and a live upload — **deferred, no deploy**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`


## Resolution

Shipped in `ba02eee5`.

Unlike every other slice this one had nothing to re-key, because there was nothing:
no table, no row, just a prefix listing. So it adds the row and puts the attribution
there, which is what keeps every S3 key already written valid forever — a child
changing toys is an `UPDATE`, never a bucket copy. `owner_key` rather than `kid_id`,
matching the workspace and memory stores, so the adoption statement that already
existed grew by one line and now covers four tables.

**It closes a live bug, not only a leak.** The listing took a single ListObjectsV2
page — 1000 objects, no continuation token — and applied the date filter to whatever
happened to be on it. A device past 1000 pictures silently lost the older ones, and a
date filter could return nothing for a day that had images. Reading a table removes
the ceiling and turns the filter into an indexed range. IST is a fixed +05:30 with no
daylight saving, so the day bounds are arithmetic; the test pins them at
`18:30Z` the previous date.

One deliberate asymmetry: the upload records the row **after** the object is safely in
S3, and never fails the upload if that write fails. The picture is already saved and
the backfill can re-read the bucket; a child has already waited for this image.

Full suite **1390 passed, 67 suites**, plus 7 new tests. Both this migration and 006's
were dry-run against the local dev database and rolled back: table and both indexes
created, and zero sessions left with a null child on a paired device.

**Not verified:** anything touching real S3. The backfill has never run, so on a fresh
deploy the gallery shows only images uploaded since — that ordering is deliberate
(migrate, deploy, then backfill) but it means the backfill's S3 pagination and its key
regex are reasoned about rather than exercised.
