---
id: SUB-9
title: "Plan change (UPI cancel + re-create)"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-8]
---

## Parent

Spec §3 change-plan; Razorpay research finding: UPI subscriptions cannot be PATCHed — plan change = cancel + create new with fresh mandate.

## What to build

`POST /api/mobile/devices/:mac/subscription/change-plan` orchestrates: create the new Razorpay subscription (new mandate approval by the parent in the portal), and only when the new sub's `activated` webhook lands, cancel the old sub and swap `plan_id`/`razorpay_subscription_id` on the row. The limbo edge is owned here: between initiating and new-mandate approval, the row stays on the old plan — a parent who abandons approval has changed nothing. Portal Manage screen grows the change-plan flow with the "you'll approve a new UPI mandate" explainer.

## Acceptance criteria

- [ ] Upgrade mid-period: old plan active until new sub activates; then new limits apply from the new billing anchor
- [ ] Abandoned mandate approval ⇒ no state change, no orphaned Razorpay sub left active
- [ ] Downgrade works identically
- [ ] Old subscription is cancelled at Razorpay after the swap (verified via test-mode API)
- [ ] Ledger records both subs' events without idempotency collisions

## Blocked by

- SUB-8
