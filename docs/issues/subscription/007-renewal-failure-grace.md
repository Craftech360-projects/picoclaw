---
id: SUB-7
title: "Renewal failure → grace → lapse (+ reconciliation)"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-15]
---

> **2026-07-21 — re-scoped to IAP rails** (RevenueCat events instead of Razorpay):
> `docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`. Original Razorpay
> scope preserved in git history.

## Parent

Spec state machine + §4; wayfinder tickets 004 (grace) + 013; IAP pivot design doc.

## What to build

The unhappy paths on RevenueCat events. `BILLING_ISSUE` ⇒ `status=grace`,
`grace_until=+3d` (or the store's `grace_period_expiration_at` if later), fix-payment FCM
push. `RENEWAL` within grace ⇒ active, clear `grace_until`. `EXPIRATION` (store retries
exhausted or grace over) ⇒ lapsed + plan-gate push. `CANCELLATION` ⇒ `cancel_at_period_end`;
`EXPIRATION` at period end ⇒ cancelled. Full-period refund (`CANCELLATION` with
`cancel_reason=CUSTOMER_SUPPORT` / refund event) ⇒ lapsed immediately.

Two safety nets: a **nightly reconciliation job** polls every `active|grace` row against the
RevenueCat REST API (`GET /subscribers/:mac`) and repairs drift with an alert on any repair;
account deletion (`DELETE /api/mobile/account`) can NOT cancel store subscriptions server-side
— instead mark rows `cancel_at_period_end` and surface "cancel in your store settings" copy in
the deletion flow.

## Acceptance criteria

- [x] Sandbox billing failure walks active→grace (AI still allowed)→lapsed with both pushes *(transitions + both pushes unit-verified; store-sandbox e2e pending SUB-17 consoles)*
- [x] `RENEWAL` during grace restores active and clears `grace_until`
- [x] Refund ⇒ immediate lapse; cancel runs out the paid period, then gates; re-subscribe reactivates
- [x] Reconciliation detects and repairs a manually-desynced row and fires the drift alert *(unit-verified against mocked RC API; live run needs `REVENUECAT_API_KEY`, SUB-17)*
- [x] Account deletion with a live sub sets period-end cancel and shows store-cancel instructions *(backend `subscription_notice`; app copy display is SUB-16)*

## Blocked by

- SUB-15

## Resolution (2026-07-21)

Shipped on `Subscription_implemetation`: commit `79c1c763` (implementation), on top of
`b50268dd` (SUB-15 code-review fixes: transactional ledger, unknown-product refusal,
guarded EXPIRATION, PRODUCT_CHANGE ledger-only, razorpay checkout gated behind
`RAZORPAY_CHECKOUT_ENABLED`, `store_product_id` unique).

- `revenuecat.service.js`: BILLING_ISSUE→grace (+3d or store window, `max()`), staleness
  guards on every unhappy-path write (`notStale` helper), EXPIRATION lapse-then-relabel
  (cancelled vs lapsed decided atomically, event can never be dropped), refund
  (`CUSTOMER_SUPPORT`) immediate lapse with plain-cancel floor, post-commit pushes.
- Lazy grace expiry in `subscription.service.js` verdict (mirrors trial expiry) — grace
  overruns are enforced even with all webhooks/crons lost.
- `jobs/rcReconciliation.js`: nightly 03:30 IST sweep of active|grace IAP rows vs RC REST
  API; repairs status/anchors/plan/cancel-flag drift; `[RC-RECONCILE][DRIFT]` is the alert
  string. Respects app-layer grace; never nulls anchors. Needs `REVENUECAT_API_KEY`.
- `deleteUserAccount`: marks rows (payer OR bound-device match) `cancel_at_period_end`
  inside the delete transaction; returns `subscription_notice` store-cancel copy.

Review: 3 rounds of 8-angle review; surviving accepted items (deliberate, commented in
code): reconciliation scoped to live rows per ticket (lapsed-but-RC-active heals on next
RENEWAL), sequential sweep (bounded concurrency when fleet outgrows the nightly window),
push helper duplicated with `subscription.service` (consolidate on third copy).
Full suite 1357/1357. E2e sandbox walk deferred to SUB-17 completion.
