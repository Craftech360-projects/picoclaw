---
id: 6
title: Razorpay Subscriptions research
type: wayfinder:research
status: closed
assignee: research-agent
blocked-by: []
---

## Question

Surface the facts the schema and lifecycle designs wait on, from Razorpay's current documentation:

1. **Subscriptions API shape**: plan objects, subscription objects, customer objects; how monthly vs annual plans are modeled; can one customer hold multiple subscriptions (per-device model needs this).
2. **UPI Autopay mandates**: amount limits (mandate cap rules for ₹199–999/mo and ~₹2000–10000/yr), auth flow, what happens on mandate revocation.
3. **Webhook events**: full lifecycle event list (`subscription.activated`, `.charged`, `.pending`, `.halted`, `.cancelled`, `.completed`, payment failures), delivery guarantees, retry behavior, signature verification.
4. **Dunning/retry**: Razorpay's built-in retry schedule on failed charges — does it fit our 3-day grace, or do we implement grace ourselves?
5. **Plan changes**: native upgrade/downgrade/proration support, or must we cancel + re-create? Pause/resume support?
6. **Trials**: does Razorpay support trial periods natively (`start_at` offset), and is that useful given our no-card-upfront trial?
7. **GST/invoicing**: what Razorpay generates vs what we must issue to parents.
8. **Test mode**: sandbox capabilities for the whole lifecycle.

Findings go to `../research/razorpay-subscriptions.md`; link from this ticket on resolution.

## Resolution (2026-07-14, research agent)

Full findings: [research/razorpay-subscriptions.md](../research/razorpay-subscriptions.md). Design-shaping facts:

1. **Plans are immutable templates**; monthly and annual tiers = separate plan objects. One subscription binds one plan for `total_count` cycles. Multiple simultaneous subscriptions per customer: implied, UNVERIFIED — each carries its own mandate (fits per-device model).
2. **UPI Autopay**: silent debits up to ₹15,000 — all our price points fit. 24h pre-debit notification precedes every charge. Customers can cancel/pause the mandate from their UPI app; cancellation is unrecoverable, customer-paused can only be customer-resumed.
3. **Webhooks**: 10 lifecycle events; **no dedicated payment-failure event** — failures surface as `pending` → `halted`. At-least-once, unordered, 24h exponential retries then auto-disabled; HMAC-SHA256 verification. Ledger must be idempotent and order-tolerant.
4. **Dunning**: retry schedule undocumented and not configurable → **our 3-day grace is built merchant-side** off `pending`/`charged`/`halted`.
5. ⚠️ **UPI subscriptions cannot be upgraded/downgraded natively** (card-only feature) — plan change on UPI = cancel + re-create with fresh mandate auth; no proration documented. Directly shapes [Schema design](012-subscription-schema-design.md) (state machine) and [Purchase UX](014-purchase-ux-prototype.md) (upgrade flow = new mandate approval).
6. **Trials**: only `start_at` offset, and payment-method capture upfront (₹5 auth) is mandatory → **our no-card-upfront 1-month trial lives entirely in our app layer**; Razorpay enters only at first purchase. Confirms [Trial lifecycle](005-trial-lifecycle-design.md)'s "purely local state" lean.
7. **GST**: Razorpay auto-generates per-cycle invoice records, but GST-compliant tax invoices remain our responsibility (UNVERIFIED whether theirs qualify).
8. **Test mode**: full lifecycle incl. forced charge success/failure + real webhook firing.
