---
id: SUB-1
title: "Walking skeleton: verdict in the session path"
type: AFK
status: open
triage: afk-ready
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

- [ ] Prisma migration adds the 4 tables; plan seed inserts 3 rows matching spec §1
- [ ] `GET /device/:mac/session-verdict` returns `{allowed, reason, remaining}`; unknown MAC with enforcement off ⇒ `allowed:true`
- [ ] Gateway calls the verdict before LiveKit dispatch on every hello; verdict outcome logged with mac + reason
- [ ] Verdict endpoint error/timeout ⇒ gateway proceeds (fail-open) and logs a fail-open event
- [ ] `ENFORCEMENT_ENABLED=false` short-circuits to allowed for every device
- [ ] End-to-end: a `client.py` voice session works unchanged with the new call in the path

## Blocked by

None — can start immediately.
