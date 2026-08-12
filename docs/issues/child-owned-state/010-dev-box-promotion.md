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

## Blocked by

- 002, 003, 004, 005, 006, 007, 008 — every behavioural slice. 001 is implied by all
  of them; 009 is independent and may land before or after
