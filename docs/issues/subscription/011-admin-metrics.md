---
id: SUB-11
title: "Admin surface, metrics & alerts"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-6]
---

## Parent

Spec §3 admin + §7 alerts; wayfinder ticket 015.

## What to build

Extend the existing password-gated `/admin-dashboard`: subscription search/view by MAC or parent, comp/extend days, and trial re-grant (sets a fresh `trial_ends_at` WITHOUT clearing the permanent `trial_used` flag) — every override writes `subscription_admin_audit` (who/when/why, before/after). A metrics page over SQL views: conversion funnel (bind→trial→paid), churn, MRR, gate-hit counts by reason. Three alert types to email/Slack: Razorpay webhook signature/delivery failures, enforcement fail-open events, and a mandate-halted spike (N in a day). Refunds stay in Razorpay's own dashboard — document the 7-day policy on the admin page, build nothing for it.

## Acceptance criteria

- [x] Comp/extend and trial re-grant work end-to-end and each produces an audit row
- [x] Re-grant leaves `trial_used=true` (no automatic future trials)
- [x] Metrics page returns funnel/churn/MRR/gate-hits from live data *(service-level live e2e; page render verified by compile, not in-browser)*
- [x] Each of the 3 alert types fires on a simulated trigger *(unit/route simulations; no live Slack post — `SLACK_ALERT_WEBHOOK_URL` unset)*
- [x] All admin subscription actions require the existing admin gate *(see Resolution: manager-web `requireSuperAdmin`, not the retired ADMIN_PASSWORD dashboard)*

## Blocked by

- SUB-6

## Resolution

Closed 2026-07-22. Commit `f16080c3` on `Subscription_implemetation` (cheeko-backend,
spans manager-api-node + manager-web).

**Reinterpretations (stated up front, Rahul-confirmed in-session):**
- Ticket predates the IAP pivot: Razorpay alert triggers → **RevenueCat webhook**
  auth/processing failures; "mandate-halted spike" → **BILLING_ISSUE daily spike**
  (`BILLING_ISSUE_SPIKE_N`, default 5); refund note documents **store refunds**.
- Per Rahul: UI lives in **manager-web** (`/subscription-admin`, HeaderBar →
  Subscriptions), not the standalone ADMIN_PASSWORD `/admin-dashboard`. Gate =
  `requireAuth` + `requireSuperAdmin` on every `/admin/subscriptions/*` route.

**Shipped:** subscriptionAdmin.service (search by MAC/parent, comp/extend,
trial re-grant that never clears `trial_used`, audit row in the same
transaction, funnel/churn/MRR/gate-hit metrics); opsAlert.service (Slack +
optional SMTP, once-per-day dedupe) with all three producers wired;
`subscription_gate_hits` table + additive migration, written fire-and-forget
from refused verdicts; Vue admin page (metrics cards, search, dialogs with
audit reason, audit trail, refund policy note).

**Evidence:** 469/469 backend unit tests (40 suites); vue build clean; live
dev-DB e2e (throwaway MAC, cleaned up): migration applied, comp +7d moved
period end, re-grant left `trial_used=true`, 2 audit rows, metrics returned
real funnel data. Review round 1: one doc-comment fix (dedupe-before-send
tradeoff); round 2 clean.

**Deploy steps (dev box):** `npx prisma generate` after pull (new model);
migration already applied to the dev DB by the e2e (idempotent SQL in
`prisma/migrations/20260722000000_sub11_gate_hits/`); set
`SLACK_ALERT_WEBHOOK_URL` (and optional `ALERT_EMAIL_TO` + SMTP vars) to make
alerts actually deliver — until then they land in pm2 logs only.
