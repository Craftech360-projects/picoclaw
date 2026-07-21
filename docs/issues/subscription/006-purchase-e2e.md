---
id: SUB-6
title: "Purchase end-to-end (Razorpay test mode)"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-2]
---

> **2026-07-21 — shelved by rails pivot.** Purchases moved in-app via Apple IAP + Google Play
> Billing (RevenueCat): `docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`.
> Replacement path: SUB-15 (backend) + SUB-16 (app) + SUB-17 (store setup).

## Parent

Spec §3 API + state machine; wayfinder tickets 006 (Razorpay research) + 012 (schema).

## What to build

A gated device becomes active through a real (test-mode) payment. Also owned here: **`GET /api/mobile/subscription/plans`** (the active catalog the portal's plans page renders). `POST /api/mobile/devices/:mac/subscription/checkout` creates the Razorpay customer + subscription for the chosen plan and returns checkout params. `POST /webhooks/razorpay` verifies HMAC-SHA256 on the raw body, inserts into `subscription_events` with `ON CONFLICT (razorpay_event_id) DO NOTHING` (dupe ⇒ 200, stop), and applies transitions for `authenticated / activated / charged`. On activation the row becomes `status=active` with the period anchored to the billing date.

Edge cases owned here: webhook may arrive **before** the checkout response returns (upsert by `razorpay_subscription_id`, never assume checkout wrote first); events arrive at-least-once and unordered (on suspected out-of-order, re-fetch subscription state from the Razorpay API and derive); seed subscriptions with a high `total_count` (~120 cycles) so `completed` never surprises.

## Acceptance criteria

- [ ] Test-mode checkout → mandate approval → device flips trial/lapsed→active; next `session-verdict` allows with plan limits
- [ ] Replayed webhook (same event id) is a no-op returning 200
- [ ] `activated` delivered before checkout's DB write still lands correctly (simulated race test)
- [ ] Invalid webhook signature ⇒ 401, no ledger row
- [ ] Bucket anchor: `current_period_start/end` match the Razorpay billing date
- [ ] Full lifecycle exercised against Razorpay test mode incl. webhook simulation
- [ ] `GET /api/mobile/subscription/plans` returns the seeded active catalog

## Blocked by

- SUB-2

## Resolution (2026-07-21)

Built and closed as **shelved, not shipped**. The full Razorpay purchase path exists on
`manager-api-node` branch `Subscription_implemetation`, commit `234bca15` ("in half ways",
user-authored): checkout endpoint, plans catalog, webhook handler with ledger dedupe +
`authenticated/activated/charged` transitions, out-of-order re-fetch, webhook-before-checkout
race handling, HMAC verify, seed script, `.env.example`. 24/24 new tests passed locally
(2026-07-21); full-suite run and review loop skipped once the pivot landed. Nothing is wired
into the live app; code stays dormant as the fallback rails if IAP economics hurt.

Criteria status: replay dedupe / invalid-signature 401 / race / anchors / plans catalog —
covered by tests. Live test-mode lifecycle — never run (no Razorpay credentials; moot after
pivot). `GET /api/mobile/subscription/plans` remains live and is reused by SUB-15/16.
