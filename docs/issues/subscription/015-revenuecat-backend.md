---
id: SUB-15
title: "RevenueCat webhook → subscription state (IAP backend)"
type: AFK
status: closed
triage: afk-ready
assignee: claude
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

- [x] Sandbox `INITIAL_PURCHASE` flips trial/lapsed → active; next `session-verdict` allows with plan limits (unit-tested transition; live sandbox delivery pending SUB-17 webhook config)
- [x] Replayed event id is a no-op returning 200
- [x] Bad/missing Authorization ⇒ 401, no ledger row; secret unset ⇒ 503
- [x] `RENEWAL` rolls `current_period_start/end` to the store billing dates; stale event cannot move anchors backward
- [x] `PRODUCT_CHANGE` swaps plan and limits apply on next verdict
- [x] Unit tests cover every mapped event + unknown-type passthrough (no live DB, SUB-6 test style)

## Blocked by

- SUB-2

## Resolution (2026-07-21)

Shipped on `manager-api-node` branch `Subscription_implemetation`:
`587095c7` (schema: `store_product_id`, `store`, `rc_original_transaction_id` + product-id
seed), `b3b4988f` (`revenuecat.service.js` + 19 unit tests — auth compare, `rc:`-prefixed
ledger dedupe, INITIAL_PURCHASE/RENEWAL with forward-only anchor guard, CANCELLATION/
UNCANCELLATION/PRODUCT_CHANGE/EXPIRATION, ledger-only default; includes a MAC-shape guard
because `normalizeMacAddress` only length-checks), `242a3054` (route + app mount + 4
integration tests). Plan: `docs/superpowers/plans/2026-07-21-sub15-revenuecat-backend.md`.

Full suite: 60 suites / 1324 tests green, 64s. Along the way `cdbb3ced` fixed the repo-wide
jest hang (three module-load `setInterval`s pinned the event loop — now `.unref()`ed).

Unverified until SUB-17: an end-to-end sandbox delivery from RevenueCat's dashboard to a
deployed endpoint (webhook + secret not yet configured). Everything above it is tested.

Unblocks: SUB-7 (grace), SUB-16 (paywall, also needs SUB-17).
