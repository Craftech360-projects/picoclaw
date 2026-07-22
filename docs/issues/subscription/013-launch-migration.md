---
id: SUB-13
title: "Launch: fleet migration, comms & kill-switch drill"
type: HITL
status: open
triage: needs-human
blocked-by: [SUB-3, SUB-10, SUB-15, SUB-16]
---

> **2026-07-21 — deps updated for the IAP pivot** (SUB-6/8 → SUB-15/16):
> `docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`.

## Parent

Spec §7 migration & rollout; wayfinder ticket 011 (fleet <100, retro-trial, kill-switch only).

## What to build

The launch itself. A seed script inserts `device_subscriptions` rows (`status=trial`, `trial_started_at=launch day`, plan=Family, `trial_used=true`) for every currently-bound MAC — every field toy gets the same fresh free month as a new one. Pre-launch comms go out (in-app announcement + one push: "plans are coming, your first month is on us"). Then the human flips `ENFORCEMENT_ENABLED` and watches the funnel dashboard and the three alert channels. Includes a rehearsed revert: flip the kill-switch off, verify every device is allowed again within one session attempt.

Migration-window edge owned here: between schema deploy and seeding, bound devices have no row — the seed MUST complete before the flag flips (verdict treats missing-row-as-lapsed once enforcement is on).

## Acceptance criteria

- [ ] Seed script idempotent (re-run creates no duplicates) and covers 100% of bound MACs *(script built + unit-tested 2026-07-22: `scripts/seed-launch-trials.js`, backend `56162382` — dry-run default, `--apply` to write, exits non-zero if any bound MAC is uncovered so the flip has a hard gate; reuses `ensureTrialForMac`'s create-if-absent upsert. Tick after the live dry-run + apply on the dev/prod box — DB unreachable from the dev laptop)*
- [ ] Comms delivered to all active parents before flip day
- [ ] Kill-switch revert drill executed: enforcement off ⇒ gated device allowed immediately
- [ ] Launch-day watch: funnel dashboard live, zero fail-open alerts in the first 24h (or each one explained)
- [ ] Post-launch validations scheduled: trial→paid conversion & bucket-consumption distribution review at day 30 (the skipped survey's replacement)

## Blocked by

- SUB-3, SUB-10, SUB-15, SUB-16
