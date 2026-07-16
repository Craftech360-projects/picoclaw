---
id: SUB-7
title: "Renewal failure → grace → lapse (+ reconciliation)"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-6]
---

## Parent

Spec state machine + §4; wayfinder tickets 004 (grace) + 013; edge-case catalog in `docs/cheeko-subscription-backend.html`.

## What to build

The unhappy paths, merchant-side (Razorpay has no configurable dunning and no payment-failure event — failures surface as `pending` → `halted`). On `subscription.pending` at renewal: `status=grace`, `grace_until=+3d`, fix-payment FCM push. `charged` within grace ⇒ back to active; `halted` or grace expiry ⇒ lapsed + plan-gate push. Map `paused` (customer paused the mandate in their UPI app — only they can resume) to the grace flow with resume-instructions copy; `resumed` ⇒ active. `refund.processed` for the full current period ⇒ lapsed immediately. Cancel flow: `POST .../subscription/cancel` sets `cancel_at_period_end`; at period end ⇒ cancelled.

Two safety nets: a **nightly reconciliation job** polls every `active|grace` subscription against the Razorpay API and repairs drift (webhooks auto-disable after 24h of failed retries) with an alert on any repair; account deletion (`DELETE /api/mobile/account`) cancels all the parent's Razorpay subscriptions before removing the account.

## Acceptance criteria

- [ ] Forced test-mode charge failure walks pending→grace (AI still allowed)→halted→lapsed with both pushes
- [ ] `charged` during grace restores active and clears `grace_until`
- [ ] `paused`/`resumed` and `refund.processed` transitions covered by tests
- [ ] Cancel runs out the paid period, then gates; re-subscribe reactivates
- [ ] Reconciliation detects and repairs a manually-desynced row and fires the drift alert
- [ ] Account deletion with a live subscription cancels the Razorpay sub first

## Blocked by

- SUB-6
