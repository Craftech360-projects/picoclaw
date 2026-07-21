---
id: SUB-15
title: "RevenueCat webhook → subscription state (IAP backend)"
type: AFK
status: open
triage: afk-ready
assignee:
blocked-by: [SUB-2]
---

## Parent

`docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md` (IAP pivot). Replaces the
Razorpay purchase path (SUB-6, shelved) as the way a device becomes `active`.

## What to build

manager-api side of IAP. `POST /webhooks/revenuecat`: verify the `Authorization` header
(shared secret, env `REVENUECAT_WEBHOOK_AUTH`), insert into `subscription_events` keyed on
RC event `id` (dupe ⇒ 200, stop), then transition `device_subscriptions` looked up by
`app_user_id` = MAC. Mapping: `INITIAL_PURCHASE`/`RENEWAL` ⇒ `active` + period from
`purchased_at_ms`/`expiration_at_ms` (anchors only move forward); `CANCELLATION` ⇒
`cancel_at_period_end=true`; `UNCANCELLATION` ⇒ clear it; `PRODUCT_CHANGE` ⇒ swap `plan_id`
by product identifier; `EXPIRATION` ⇒ `lapsed`. `BILLING_ISSUE` is ledgered only (SUB-7 owns
grace). Unknown/future event types: ledger + 200.

Schema migration: `device_subscriptions` + `store` enum (`app_store|play_store`) +
`rc_original_transaction_id`. Map RC `product_id` → `subscription_plans.tier` via a new
`store_product_id` column on `subscription_plans` (seeded for the 3 tiers).

Webhook-before-bind edge: upsert the row by MAC (same rule as the Razorpay handler — never
assume bind wrote first).

## Acceptance criteria

- [ ] Sandbox `INITIAL_PURCHASE` flips trial/lapsed → active; next `session-verdict` allows with plan limits
- [ ] Replayed event id is a no-op returning 200
- [ ] Bad/missing Authorization ⇒ 401, no ledger row; secret unset ⇒ 503
- [ ] `RENEWAL` rolls `current_period_start/end` to the store billing dates; stale event cannot move anchors backward
- [ ] `PRODUCT_CHANGE` swaps plan and limits apply on next verdict
- [ ] Unit tests cover every mapped event + unknown-type passthrough (no live DB, SUB-6 test style)

## Blocked by

- SUB-2
