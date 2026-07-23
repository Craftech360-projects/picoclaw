---
id: SUB-18
title: "Admin dashboard: device detail drawer + quick wins + refund lookup"
type: AFK
status: open
triage: afk-ready
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

- [ ] Results table shows grace countdown, cancel-at-period-end tag, plan tier+price, phone
- [ ] Clicking a row opens a drawer with status+reason, plan+limits, period/trial/grace,
      store+RC txn id, usage-vs-limits bars, event timeline, audit history
- [ ] Usage-vs-limits numbers match the verdict's (shared helper, no drift)
- [ ] Searching an `rc_original_transaction_id` finds the device
- [ ] `/detail` is superadmin-gated; unknown MAC → clean 404, no-plan MAC → sensible empty state

## Blocked by

- (none)
