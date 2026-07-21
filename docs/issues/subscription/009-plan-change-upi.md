---
id: SUB-9
title: "Plan change (store-native upgrade/downgrade)"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-16]
---

> **2026-07-21 — re-scoped to IAP rails.** UPI cancel+recreate orchestration is gone; stores
> handle proration natively. `docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`.
> Original Razorpay scope preserved in git history.

## Parent

IAP pivot design doc; Apple subscription-group upgrade/downgrade; Google replacement modes.

## What to build

App side: change-plan UI on Manage — `purchaseStoreProduct` on the new tier's package
(same Apple subscription group / Google `CHARGE_PRORATED_PRICE` replacement). Backend side:
already-mapped `PRODUCT_CHANGE` (SUB-15) swaps `plan_id`; verify limits/buckets apply from
the store-decided effective moment (Apple: upgrade immediate, downgrade at period end — the
webhook timing encodes this, no extra logic).

## Acceptance criteria

- [ ] Sandbox upgrade applies new limits on next verdict after the `PRODUCT_CHANGE` lands
- [ ] Sandbox downgrade keeps old limits until period end, then switches
- [ ] Abandoned store sheet ⇒ no state change anywhere
- [ ] Ledger records the change without idempotency collisions

## Blocked by

- SUB-16
