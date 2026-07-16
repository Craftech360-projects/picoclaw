---
id: SUB-6
title: "Purchase end-to-end (Razorpay test mode)"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-2]
---

## Parent

Spec §3 API + state machine; wayfinder tickets 006 (Razorpay research) + 012 (schema).

## What to build

A gated device becomes active through a real (test-mode) payment. `POST /api/mobile/devices/:mac/subscription/checkout` creates the Razorpay customer + subscription for the chosen plan and returns checkout params. `POST /webhooks/razorpay` verifies HMAC-SHA256 on the raw body, inserts into `subscription_events` with `ON CONFLICT (razorpay_event_id) DO NOTHING` (dupe ⇒ 200, stop), and applies transitions for `authenticated / activated / charged`. On activation the row becomes `status=active` with the period anchored to the billing date.

Edge cases owned here: webhook may arrive **before** the checkout response returns (upsert by `razorpay_subscription_id`, never assume checkout wrote first); events arrive at-least-once and unordered (on suspected out-of-order, re-fetch subscription state from the Razorpay API and derive); seed subscriptions with a high `total_count` (~120 cycles) so `completed` never surprises.

## Acceptance criteria

- [ ] Test-mode checkout → mandate approval → device flips trial/lapsed→active; next `session-verdict` allows with plan limits
- [ ] Replayed webhook (same event id) is a no-op returning 200
- [ ] `activated` delivered before checkout's DB write still lands correctly (simulated race test)
- [ ] Invalid webhook signature ⇒ 401, no ledger row
- [ ] Bucket anchor: `current_period_start/end` match the Razorpay billing date
- [ ] Full lifecycle exercised against Razorpay test mode incl. webhook simulation

## Blocked by

- SUB-2
