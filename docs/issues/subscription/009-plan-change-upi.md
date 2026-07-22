---
id: SUB-9
title: "Plan change (store-native upgrade/downgrade)"
type: AFK
status: closed
triage: afk-ready
assignee: claude
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

- [ ] Sandbox upgrade applies new limits on next verdict after the `PRODUCT_CHANGE` lands *(unit-verified end-to-end: backend swaps plan on the effective-time event, app polls until the flip; real store-sandbox run deferred to SUB-17 verification — Test Store can't reproduce subscription-group/replacement-mode semantics)*
- [ ] Sandbox downgrade keeps old limits until period end, then switches *(unit-verified: DEFERRED replacement mode + no client poll + backend leaves the row untouched until the period-end transaction; store-sandbox run deferred to SUB-17)*
- [x] Abandoned store sheet ⇒ no state change anywhere *(unit-tested: cancelled sheet → flow idle, no notice/error, no refetch; nothing server-side by construction — no commit, no webhook)*
- [x] Ledger records the change without idempotency collisions *(backend unit test: PRODUCT_CHANGE + effective RENEWAL ledger under distinct `rc:` ids, dedupe intact)*

## Blocked by

- SUB-16

## Resolution (2026-07-22)

Shipped both sides; review-clean (multi-angle review, round-2 verified).

**App** (`CheekoAI-Parent-App` @ `feat/iap-subscription`, commit `ca5503c`): change-plan
cards on the Manage view → `purchaseStoreProduct` on the new tier's package. Google gets
`GoogleProductChangeInfo` — CHARGE_PRORATED_PRICE for upgrades, **DEFERRED for downgrades**
(prorated-price is upgrade-only per Play Billing; spec's blanket CHARGE_PRORATED_PRICE was
wrong for downgrades). Apple swaps natively within the subscription group. Upgrades poll the
summary until the plan flips; downgrades show a period-end notice, no poll. Fail-closed guard
refuses the change when the current plan's store product id or tier order is unknown (a null
replacement would start a second Play subscription = double billing). Review fixes folded in:
shared `_commitStorePurchase` (restores already-subscribed mapping on change), no downgrade
refetch (null response could bounce the user to the paywall), stale-notice clearing, shared
`_FlowStatus` widget. App tests 22/22 subscription-related green; analyzer baseline unchanged.

**Backend** (`manager-api-node` @ `deploy/otadev-subscription`, commit `01695a10`): no code
change needed — SUB-15 already handles it (PRODUCT_CHANGE ledger-only; effective-time
RENEWAL/INITIAL_PURCHASE carries the new `product_id` and swaps `plan_id` under the
forward-only anchor guard). Added the SUB-9 contract test; suite 473/473 green.

**Deferred to SUB-17 verification (with the rest of the real-store e2e):** live sandbox
upgrade/downgrade runs — including confirming RC's effective-time event actually lands within
the app's ~20s poll window on an immediate upgrade (review flagged this as the one
timing assumption only a sandbox run can prove; the timeout copy is honest if it lags).
