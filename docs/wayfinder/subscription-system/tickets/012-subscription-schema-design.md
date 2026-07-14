---
id: 12
title: Subscription schema design
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: [6, 10]
---

## Question

Design the Prisma models (manager-api) for the subscription system. Blocked by Razorpay research (webhook/object shapes inform columns) and the packaging decision (what a "bucket" column means).

Must cover:

1. **Plan catalog** — `subscription_plans` (tier, price monthly/annual, question bucket, daily caps, image limits, feature flags like memory/kid-profiles) — DB-driven so prices change without deploys.
2. **Device subscription state** — current plan, status (`trial|active|grace|lapsed|cancelled`), trial_started_at / trial_used (permanent, MAC-anchored per [Trial shape](003-trial-shape.md)), period start/end, Razorpay subscription/customer ids. ⚠️ **Must be keyed by MAC in its own table, NOT rows on `ai_device`** — unbind deletes the `ai_device` row (`device.service.js:184`, found in [Trial lifecycle design](005-trial-lifecycle-design.md)); trial/subscription state must survive unbind/rebind.
3. **Payment/event ledger** — webhook events + charges, idempotent by Razorpay event id.
4. **Usage linkage** — how monthly question buckets are computed from `device_token_usage_session` (live aggregate vs counter column; consider the existing daily-rollup pattern).
5. State machine: every transition (trial→active, active→grace→lapsed, cancel-at-period-end, upgrade/downgrade timing) drawn explicitly.
6. Relation to existing `ai_device`, `parent_profile`, and the Phase 2 `daily_minutes_limit` proposal in `docs/plan-usage-tracking-and-limits.md` §2.1 (subsume it — limits come from the plan row, not per-device fields).

## Resolution (2026-07-14, grilling session)

**Billing cycle: monthly-only at launch** (user's call — 3 Razorpay plan objects; the portal hides the prototype's annual toggle until annual ships). Four new Prisma models, snake_case like the existing ~65:

**`subscription_plans`** (DB-driven catalog — price changes without deploys)
`id, tier ('starter'|'family'|'premium', unique), name, price_inr, monthly_question_limit, daily_question_limit, daily_minutes_limit (invisible backstop), monthly_image_limit (null = unlimited), daily_image_limit, features (JSON: memory, kid_profiles, insights_level), razorpay_plan_id, is_active, created_at, updated_at`

**`device_subscriptions`** (the spine — **keyed by `mac_address` UNIQUE, not FK to `ai_device`**, since unbind deletes that row)
`id, mac_address (unique), status ('trial'|'active'|'grace'|'lapsed'|'cancelled'), plan_id FK (Family during trial), user_id (payer; nullable, survives unbind), trial_started_at, trial_ends_at, trial_used (bool, permanent once true), billing_cycle ('monthly'), current_period_start, current_period_end (bucket anchor = billing anniversary per [Packaging](010-packaging-decision.md)), grace_until (nullable), cancel_at_period_end (bool), razorpay_customer_id, razorpay_subscription_id (nullable — null during trial), created_at, updated_at`

**`subscription_events`** (webhook ledger — idempotent, order-tolerant per [Razorpay research](006-razorpay-subscriptions-research.md))
`id, razorpay_event_id (unique — dedupe key), event_type, mac_address, razorpay_subscription_id, payload (JSON), processed_at, created_at`

**`subscription_admin_audit`** (per [Admin & ops](015-admin-ops-surface.md))
`id, admin_user, action ('comp_extend'|'trial_regrant'|'plan_override'), mac_address, reason, before_state (JSON), after_state (JSON), created_at`

**Usage computation: live aggregates, no counter columns** — monthly questions = `SUM(message_count)` over `device_token_usage_session` where `created_at >= current_period_start`; daily = same over IST day; images counted from the imagine-upload path. At <100 devices this is trivial; add a counter/rollup only if the verdict query ever shows up in latency. <!-- ponytail: live SUM, rollup table if fleet × sessions makes it slow -->

**State machine** (every transition):
```
(none) ──first bind──▶ trial(trial_ends_at=+30d, plan=family)
trial ──expiry──▶ lapsed                    trial ──purchase──▶ active
active ──subscription.pending @renewal──▶ grace(grace_until=+3d)
grace ──subscription.charged──▶ active      grace ──grace_until passes / halted──▶ lapsed
active ──cancel──▶ cancel_at_period_end=true ──period end──▶ cancelled
lapsed|cancelled ──purchase──▶ active       UPI plan change = cancel current Razorpay sub + create new (one row, new razorpay_subscription_id)
```

**Phase 2 §2.1 subsumed**: no per-device limit fields — the gateway's verdict endpoint reads limits off the device's plan row (trial ⇒ Family limits). Field-fleet migration = one seed script inserting `device_subscriptions` rows (status=trial, trial_started_at=launch day) for all currently-bound MACs per [Field-device migration](011-field-device-migration.md).
