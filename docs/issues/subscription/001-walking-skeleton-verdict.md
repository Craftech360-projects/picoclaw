---
id: SUB-1
title: "Walking skeleton: verdict in the session path"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: []
---

## Parent

`docs/cheeko-subscription-spec.md` (§2 schema, §3 API, §5 kill-switch) — decision trail in `docs/wayfinder/subscription-system/`.

## What to build

The thinnest path that puts subscription checking into every voice session without changing behavior: create the four subscription tables, seed the plan catalog (Starter/Family/Premium from spec §1), expose `GET /device/:mac/session-verdict` on manager-api (service-key auth), and have the mqtt-gateway call it in its deferred session setup before creating the LiveKit room. With `ENFORCEMENT_ENABLED=false` (default), the verdict always returns `allowed:true` — sessions behave exactly as today, but every session start now logs a verdict.

Schema (from the spec, decision-rich shape — trimmed):

```
subscription_plans(id, tier UNIQUE, name, price_inr, monthly_question_limit,
  daily_question_limit, daily_minutes_limit, monthly_image_limit NULL=∞,
  daily_image_limit, features JSON, razorpay_plan_id, is_active)
device_subscriptions(id, mac_address UNIQUE, status trial|active|grace|lapsed|cancelled,
  plan_id FK, user_id, trial_started_at, trial_ends_at, trial_used,
  billing_cycle, current_period_start, current_period_end, grace_until,
  cancel_at_period_end, razorpay_customer_id, razorpay_subscription_id)
subscription_events(id, razorpay_event_id UNIQUE, event_type, mac_address,
  razorpay_subscription_id, payload JSON, processed_at)
subscription_admin_audit(id, admin_user, action, mac_address, reason,
  before_state JSON, after_state JSON)
```

Key rule: `device_subscriptions` is keyed by MAC, never FK'd to `ai_device` — unbind deletes that row and subscription state must survive.

## Acceptance criteria

- [x] Prisma migration adds the 4 tables; plan seed inserts 3 rows matching spec §1
- [x] `GET /device/:mac/session-verdict` returns `{allowed, reason, remaining}`; unknown MAC with enforcement off ⇒ `allowed:true`
- [x] Gateway calls the verdict before LiveKit dispatch on every hello; verdict outcome logged with mac + reason
- [x] Verdict endpoint error/timeout ⇒ gateway proceeds (fail-open) and logs a fail-open event
- [x] `ENFORCEMENT_ENABLED=false` short-circuits to allowed for every device
- [x] End-to-end: a `client.py` voice session works unchanged with the new call in the path

## Blocked by

None — can start immediately.

## Resolution

Shipped in `cheeko-backend@dcadc790` (branch `Subscription_implemetation`); the
`client.py` verdict trace rides this repo's `feat/tts-sentence-audio-pacing`.

**What shipped**

- Migration `20260716000000_add_subscription_tables` — 4 tables + 3-row plan
  seed, inline via `INSERT … ON CONFLICT DO NOTHING` (matches the
  `image_providers` house style; no separate seed script). `status` is a
  `VARCHAR` + CHECK, since this schema uses no enums.
- `subscription.service.js` `getSessionVerdict()` + `GET /device/:mac/session-verdict`
  behind `requireServiceKey`.
- Gateway `fetchSessionVerdict()` inside `_deferredSetup`'s existing parallel
  batch — no added wall-clock — fails open on any error.
- `client.py` logs the verdict before hello and after hello (mirrors the
  gateway; the real device never asks for it).

**Verification** — migration applied and re-applied cleanly on a throwaway
Postgres (idempotent, CHECK + MAC-UNIQUE enforced) and on the dev DB. Live
servers: 401 without/with a wrong service key; enforcement off ⇒ unknown MAC
allowed; enforcement on ⇒ trial/active/grace allowed, lapsed/cancelled
`no_plan`; gateway logged the verdict at `.377` and created the room at `.450`
(call genuinely precedes dispatch); a 401 verdict logged `VERDICT-FAIL-OPEN` and
still dispatched the agent. 15 jest tests + 6 node:test tests green.

**Deferred / notes**

- Refusal behavior is SUB-2's (`no LiveKit room is created`), so SUB-1 observes
  and logs only — a `lapsed` device still gets a room today. That is intended.
- `remaining` is reported as all-null (unknown), not zero — SUB-3 computes the
  real buckets from `device_token_usage_session`.
- `daily_image_limit` for Starter (15) and Premium (NULL) are **assumptions**:
  spec §1 gives Starter 150/mo and "Premium unlimited" but no daily figure.
  Tunable in-DB without a deploy; confirm before launch (SUB-13).
- `razorpay_plan_id` seeds NULL — populated when the Razorpay plan objects exist
  (SUB-6).
- Not fixed, out of scope: gateway silently fails open forever if both
  `MANAGER_API_SECRET` and `SERVICE_SECRET_KEY` are unset (a config typo would
  disable enforcement with only a per-session warn). Worth a startup warn in
  SUB-2, when refusal actually starts mattering.
- Pre-existing and untouched: 2 failures in the gateway's
  `dispatch-metadata.test.js` (confirmed present with these changes stashed).
