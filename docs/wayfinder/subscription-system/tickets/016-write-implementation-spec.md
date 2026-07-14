---
id: 16
title: Write the implementation spec (destination)
type: wayfinder:task
status: closed
assignee: rahul
blocked-by: [5, 9, 10, 11, 12, 13, 14, 15]
---

## Question

Assemble the destination artifact: `docs/cheeko-subscription-spec.md` — the full implementation spec synthesizing every closed ticket:

- Decisions summary (rails, unit, trial, lapse, packaging) with links back to tickets.
- DB schema (from [Schema design](012-subscription-schema-design.md)) + subscription state machine.
- API surface: manager-api endpoints (mobile purchase/manage, gateway verdict check, Razorpay webhooks) with request/response shapes.
- Trial lifecycle + notifications timeline.
- Enforcement flow diagrams (voice, imagine, music/story).
- Parent-app screens (from the prototype) + admin surface.
- Field-device migration & staged rollout plan.
- Phased implementation plan with verify steps per phase, ready for `/to-issues`.

Resolution = the spec exists, is reviewed by the human, and the map closes.

## Resolution (2026-07-14)

**Spec written: [`docs/cheeko-subscription-spec.md`](../../cheeko-subscription-spec.md)** — decisions table (all 16 tickets linked), plans, 4-model schema + state machine, full API surface (gateway/portal/webhook/admin), trial lifecycle + notification matrix, enforcement flows, portal + app scope (direction A), migration steps, and an 8-phase implementation plan with per-phase verify steps, ready for `/to-issues`. Pending: human review of the spec.
