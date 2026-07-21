---
id: SUB-7
title: "Renewal failure → grace → lapse (+ reconciliation)"
type: AFK
status: open
triage: afk-ready
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

- [ ] Sandbox billing failure walks active→grace (AI still allowed)→lapsed with both pushes
- [ ] `RENEWAL` during grace restores active and clears `grace_until`
- [ ] Refund ⇒ immediate lapse; cancel runs out the paid period, then gates; re-subscribe reactivates
- [ ] Reconciliation detects and repairs a manually-desynced row and fires the drift alert
- [ ] Account deletion with a live sub sets period-end cancel and shows store-cancel instructions

## Blocked by

- SUB-15
