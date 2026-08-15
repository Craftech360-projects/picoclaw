---
status: open
assignee:
---

# 010 — Dev box promotion

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

Human-in-the-loop. Promote the whole phase to the DO dev box (`64.227.170.31`,
DB1 `tsiocygczplmnjpqmutc`) and verify it against real sessions. Dev only — never
production (`139.59.7.72`) and never EKS.

Three services, and the order matters because two of them share a repo on the box:

- `manager-api` and `mqtt-gateway` both from `/root/xiaozhi-esp32-server`
- `picoclaw-livekit` from `/root/picoclaw`, built on the box (cgo/opus, local
  Windows cross-compile fails)

**The migration hazard.** `server.js` runs `runPrismaMigrations()` on boot and DB1
has `_prisma_migrations`, so `pm2 restart manager-api` applies **every** unapplied
migration file in the tree in one go. A prior promotion applied an expand/contract
pair 0.26 seconds apart, skipping the soak that was supposed to sit between them.
Any migration that should not run yet must not be in the tree at restart time.
Run `npx prisma generate` before restarting, or `select: { owner_key }` throws at
runtime.

**Re-run the survey first.** The zero-results that make this migration safe — no kid
on two devices, no device whose history predates its current child — were measured on
2026-08-12. Re-run both against DB1 immediately before the backfill. A non-zero
result means the backfill needs a dedupe pass and this ticket stops for a decision.

Verification is by live session, not by inspection. The two biggest findings of the
per-age-banks build were invisible in code and only surfaced in logs and DB rows.

## Acceptance criteria

- [ ] Local HEAD matches server HEAD in both repos before anything ships
- [ ] Survey queries re-run against DB1 and results recorded in this ticket; a
      non-zero result on either stops the promotion
- [ ] Migrations applied deliberately, with `prisma generate` run before the restart
- [ ] Pairing count on DB1 confirmed at 23 of 24 after 001 lands
- [ ] Backfill leaves zero unattributed rows across quiz, riddle, artifacts, memory
      documents, memory chunks, voice sessions, imagine and the seven live rollups
- [ ] One live **Quizzy** session and one live **Riddler** session, each verified from
      the DB: answers carry the right child, the injected prompt shows the right level
- [ ] A device re-paired to a second child on the box: the new child gets a clean
      workspace, empty progress, an empty gallery — and the first child's rows are
      still intact and still theirs
- [ ] The first child re-paired to a **different** device recovers memory, workspace,
      progress, chat history and gallery
- [ ] Worker workspace directories on the box are `workspace-kid-<id>` for paired
      devices
- [ ] `detailed_trace_enabled` reviewed — it is still on, and it puts children's
      conversation content in plaintext logs

## Survey re-run — DB1, 2026-08-13

Read-only, run from the box against DB1 before anything shipped. Three gates
clean, one not.

| Gate | 2026-08-12 | 2026-08-13 | Verdict |
|---|---|---|---|
| Kids on more than one device | 0 | **0** | clean |
| Quiz MACs with no `ai_device` row | 0 | **0** | clean |
| Artifact / memory-doc MACs with no `ai_device` row | 0 | **0** | clean |
| Devices whose history predates their current child | 0 | **4** | **STOP** |

Scale unchanged and small: 24 devices (6 paired), 11 kids, 152 artifacts, 796
memory documents, 640 voice sessions, 38 quiz answers. None of the nine
migrations are applied to DB1; `imagine_image` does not exist there yet.
`parent_profile` already has both consent columns, so `20260813000000` is a
no-op on this database.

### Why the fourth gate is not a false alarm

`created_at` was only ever a proxy for "could the backfill mis-attribute this?".
`device_memory_documents` and `voice_sessions` already carry `kid_id` on DB1, so
the question can be asked directly — and the answer is that two devices hold rows
belonging to children other than the one they are paired to now:

| Device | Paired to | Rows belonging to another child |
|---|---|---|
| `00:16:3E:AC:B5:38` | kid 14 | kid 1: 15 memory docs + 14 sessions · kid 6: 67 memory docs + 71 sessions |
| `68:EE:8F:60:BC:28` | kid 12 | kid 10: 2 memory docs + 1 session |

The other two flagged devices (`68:EE:8F:60:BC:00`, `14:C1:9F:D6:44:F4`) have a
single-child owner and no foreign `kid_id` anywhere — their artifacts simply
predate a profile created later. Those are lossless.

`00:16:3E:AC:B5:38` has been shared by three children. The backfill rule as
written — attribute every existing row to the device's current child — would
relabel 82 of kid 1's and kid 6's memory documents as kid 14's. For
`device_workspace_artifacts` that is unrecoverable: the table has no `kid_id`
today, so once `owner_key` is written there is no record of who the rows were.

### The decision this ticket is waiting on

1. **Which tables the backfill may overwrite.** If it fills `owner_key` only from
   an existing `kid_id` where one is present, and falls back to the device's
   current child only where it is null, both co-mingled devices survive intact and
   the promotion can proceed. This needs checking against the actual backfill, not
   assumed.
2. **How to split `device_workspace_artifacts` on `00:16:3E:AC:B5:38`.** The
   memory documents carry `kid_id` and timestamps, so the artifact rows can be
   attributed by time range against them — but that is a dedupe pass someone has
   to write and eyeball, and it is the only signal available.

### Resolved — the backfill was fixed, then the promotion ran

`20260812040000` was reaching past a recorded fact to an inference:
`device_memory_documents` and `device_memory_chunks` already carry `kid_id`, and
the update stamped every row on a paired device with the device's *current*
child. The collapse that followed then resolved the collisions that
mis-attribution manufactured, by DELETE.

Fixed in `4c330634` before anything was applied. Rows that know their child keep
it; rows that do not are claimed for the current child only where no sibling
table names a different one; the collapse is now an assertion that stops the
migration rather than dropping a row. The precondition is computed by the
migration itself instead of citing a survey run by hand against another database
— which is what let this reach a box in the first place.

**Applied to DB1 on 2026-08-13** via `prisma migrate deploy` (10 migrations: the
nine plus `20260813000000`), then `prisma generate`, then a deliberate restart.

Nothing was lost. Artifacts 152 → 152, chunks 791 → 791, voice sessions 640 →
640, quiz answers 38 → 38. Memory documents 796 → 798, the two rows
`20260812050000` inserts by design. Zero NULL `owner_key` across all three
tables, and the collision assertion never fired — the reasoning held.

The co-mingled device split the way it should:

| `00:16:3E:AC:B5:38` | before | after |
|---|---|---|
| 15 docs, kid 1 | — | `kid:1` |
| 67 docs, kid 6 | — | `kid:6` |
| 3 docs, kid 14 | — | `kid:14` |
| 20 docs, no kid | — | `mac:00:16:3e:ac:b5:38` |
| 11 artifacts | — | `mac:00:16:3e:ac:b5:38` |

Under the original backfill all 105 documents and 11 artifacts would have become
kid 14's, and the ones colliding on `document_key` would have been deleted.

Both services are on `feat/child-owned-state` and restarted: `manager-api`
(`4c330634`) and `picoclaw-livekit`, built on the box — the first time that
binary has linked anywhere, since the local Windows build fails on libolm cgo.
`imagine_image` now exists on DB1, empty.

### Still open

- **Pairing is 6 of 24, not the 23 of 24 this ticket expects.** The migrations
  record existing pairings into `device_kid_assignment` (6 rows) but nothing
  auto-pairs the 17 devices whose owner has exactly one child. Either that step
  is a code path that only runs at bind time, or the promotion is missing a
  backfill. Worth settling before the live-session checks.
- Live Quizzy and Riddler sessions on the box, the re-pair tests, and the
  `workspace-kid-<id>` directory check are all human-in-the-loop and unstarted.
- `scripts/backfill-imagine-images.js` has still never run anywhere.
- `detailed_trace_enabled` is still on.

## Blocked by

- 002, 003, 004, 005, 006, 007, 008 — every behavioural slice. 001 is implied by all
  of them; 009 is independent and may land before or after
