---
id: 15
title: Admin & ops surface
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

What do operators need on day one?

1. **Admin dashboard** (manager-api already has one, password-gated): view/search device subscriptions, manual plan override, comp/extend a subscription, support-side trial re-grant policy (trial is once-per-MAC by [Trial shape](003-trial-shape.md) — is support allowed to override for returns/refurb, and how is that audited?).
2. **Refunds**: policy + who executes (Razorpay dashboard vs our admin UI).
3. **Metrics**: conversion funnel (activation → trial → paid), churn, MRR, gate-hit rates — what's computed where (SQL views? daily rollup like existing `device_usage_daily`?).
4. **Alerts**: webhook-delivery failures, mandate-halted spikes, enforcement fail-open events.

## Resolution (2026-07-14, grilling session)

Context: existing admin dashboard is a static `ADMIN_PASSWORD`-gated persona editor at `/admin-dashboard` (`manager-api src/app.js:152`) — subscription admin extends it.

1. **Admin powers**: view/search device subscriptions + **comp/extend days + trial re-grant** for legit cases (returns/refurb) — every override writes an audit row (who/when/why). No Razorpay-touching actions in our UI.
2. **Refunds**: executed manually in the **Razorpay dashboard**; our webhook handler reflects the resulting state. Spec documents the refund policy (e.g. 7-day no-questions) — nothing built.
3. **Metrics**: **SQL views + one plain admin page** — conversion funnel (bind→trial→paid), churn, MRR, gate-hit counts. Trial-conversion rate is THE launch health metric; one click, no BI stack.
4. **Alerts** (email/Slack, no pager infra): Razorpay webhook signature/delivery failures, enforcement **fail-open** events (gateway couldn't reach manager-api → unmetered sessions), **and mandate-halted spikes** (user chose to include — catches UPI outages early).
