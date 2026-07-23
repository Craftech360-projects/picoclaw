---
id: SUB-18
title: "Admin dashboard: device detail drawer + quick wins + refund lookup"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: []
---

## Parent

`docs/issues/subscription/subscription-admin-dashboard-plan.md` (Phase 1 + quick wins +
Phase 3). Converts the read-only monitor into a support console — the recommended first PR.

## What to build

**Quick wins (no backend change):** in `SubscriptionAdmin.vue`'s results table, render fields
already returned by the search/list payload but not shown — grace countdown (`grace_until`),
a "cancelling at period end" tag (`cancel_at_period_end`), plan tier + price (only `name`
shown today), parent phone.

**Detail drawer (new read endpoint):** click a row → `el-drawer` for that MAC showing:
- status + *why gated* (bucket reason) — reuse `subscription.service` verdict/usage helpers,
  do NOT re-implement bucket math.
- plan + all limits (question/minute/image, daily+monthly).
- period dates, live grace countdown, trial history, `cancel_at_period_end`.
- `store` + `rc_original_transaction_id`.
- live usage-vs-limits (questions month/today, minutes today, images today) as used/limit bars.
- `subscription_events` timeline for the MAC (type, time, processed_at), newest first.
- this device's audit history.

Backend: `GET /admin/subscriptions/:mac/detail` (requireAuth + requireSuperAdmin) assembling
the above from the subscription/usage helpers + the events ledger.

**Refund lookup (Phase 3):** extend `searchSubscriptions` q-match to include
`rc_original_transaction_id`, so an admin can find a device by its RevenueCat txn id and, in
the drawer's event timeline, confirm the store CANCELLATION/EXPIRATION landed. Replaces the
static Refunds blurb with a real lookup path.

## Acceptance criteria

- [x] Results table shows grace countdown, cancel-at-period-end tag, plan tier+price, phone
- [x] Clicking a row opens a drawer with status+reason, plan+limits, period/trial/grace,
      store+RC txn id, usage-vs-limits bars, event timeline, audit history
- [x] Usage-vs-limits numbers match the verdict's (shared helper, no drift)
- [x] Searching an `rc_original_transaction_id` finds the device
- [x] `/detail` is superadmin-gated; unknown MAC → clean 404, no-plan MAC → sensible empty state

## Blocked by

- (none)

## Resolution

Shipped in `cheeko-backend@84a2984b` (branch `deploy/otadev-subscription`). Converts the
read-only monitor into a support console.

**Backend** (`manager-api-node`):
- `GET /admin/subscriptions/:mac/detail` (requireAuth + requireSuperAdmin) — assembles
  status + why-gated, plan+limits, period/trial/grace, `store`+`rc_original_transaction_id`,
  usage-vs-limits, the `subscription_events` timeline (newest first, `id` tiebreaker), and
  this MAC's audit history. Reuses `getSubscriptionSummary` + a **dry-run** `getSessionVerdict`
  so the drawer's numbers can't drift from enforcement (no re-implemented bucket math).
- `getSessionVerdict` gained a `dryRun` flag: computes the true verdict even with the
  kill-switch off and skips the gate-hit ledger — for admin reads only.
- `searchSubscriptions` now also matches `rc_original_transaction_id` (refund lookup),
  folding in MACs found only by their RC txn id.

**Frontend** (`manager-web/SubscriptionAdmin.vue`): quick-win columns (grace/cancelling
tags, plan name+tier+price, parent phone), row-click `el-drawer` with all sections + usage
`el-progress` bars, refund-lookup note replacing the static blurb.

**Verification:** backend unit + integration tests green (90 subscription tests, incl. new
dry-run, `getDetail` compose/404/empty-shell, and RC-txn search cases); Vue template compiles
clean. Note: the frontend was verified by template compilation + code review, not a live
browser session (no live dashboard/DB in this environment).

**Review:** `/code-review` (high) surfaced 5 findings — 4 accepted as the ticket's mandated
reuse tradeoff (read-path lazy-expiry side effect, duplicate row reads, 2×limit search rows
that deliberately preserve the exact RC match, static countdown snapshot); 1 fixed (added an
`id` tiebreaker to the event ordering).

**Deferred (Phase 2+):** write actions from the drawer (SUB-19), metrics/trends (SUB-20).
