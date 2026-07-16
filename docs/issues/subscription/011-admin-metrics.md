---
id: SUB-11
title: "Admin surface, metrics & alerts"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-6]
---

## Parent

Spec §3 admin + §7 alerts; wayfinder ticket 015.

## What to build

Extend the existing password-gated `/admin-dashboard`: subscription search/view by MAC or parent, comp/extend days, and trial re-grant (sets a fresh `trial_ends_at` WITHOUT clearing the permanent `trial_used` flag) — every override writes `subscription_admin_audit` (who/when/why, before/after). A metrics page over SQL views: conversion funnel (bind→trial→paid), churn, MRR, gate-hit counts by reason. Three alert types to email/Slack: Razorpay webhook signature/delivery failures, enforcement fail-open events, and a mandate-halted spike (N in a day). Refunds stay in Razorpay's own dashboard — document the 7-day policy on the admin page, build nothing for it.

## Acceptance criteria

- [ ] Comp/extend and trial re-grant work end-to-end and each produces an audit row
- [ ] Re-grant leaves `trial_used=true` (no automatic future trials)
- [ ] Metrics page returns funnel/churn/MRR/gate-hits from live data
- [ ] Each of the 3 alert types fires on a simulated trigger
- [ ] All admin subscription actions require the existing ADMIN_PASSWORD gate

## Blocked by

- SUB-6
