---
id: SUB-19
title: "Admin dashboard: write actions (cancel/uncancel, set-status, change-plan)"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-18]
---

## Parent

`docs/issues/subscription/subscription-admin-dashboard-plan.md` (Phase 2). Adds the
support-desk escape hatches, driven from the detail drawer built in SUB-18.

## What to build

Three new audited admin actions, each surfaced as a button in the detail drawer with a
confirm dialog and a required reason:
- **Cancel / uncancel** — toggle `cancel_at_period_end` (repair a store desync).
- **Set-status override** — force `lapsed` / reactivate to `active`|`trial` (support escape
  hatch when a webhook was missed).
- **Change plan** — re-point `plan_id` (correct a mis-mapped product).

Backend: `POST /admin/subscriptions/:mac/cancel`, `.../status`, `.../plan`
(requireAuth + requireSuperAdmin). Each validates input, applies the change, and writes a
`subscription_audit` row with before/after — mirroring the existing `compExtend`/`regrantTrial`
pattern. Never a bare UPDATE: the audit trail is the accountability record.

## Acceptance criteria

- [x] Cancel sets `cancel_at_period_end=true`; uncancel clears it; both audited with reason
- [x] Set-status override changes status and writes before/after audit; guarded by confirm
- [x] Change-plan re-points `plan_id`; next verdict applies the new plan's limits
- [x] Every action requires a reason and appears in the audit trail (and the drawer's history)
- [x] All three endpoints are superadmin-gated; invalid inputs → 400, unknown MAC → 404

## Blocked by

- SUB-18

## Resolution

Shipped in `cheeko-backend@adbc7661` (branch `deploy/otadev-subscription`).

**Backend** (`subscriptionAdmin.service.js` + `admin.routes.js`): `setCancelAtPeriodEnd`,
`setStatusOverride`, `changePlan` — each mirrors the `compExtend` contract (transaction,
404 on missing row, `subscription_admin_audit` with before/after in the same transaction;
the actual audit table — the ticket's `subscription_audit` name was shorthand). Reason is
**mandatory** on all three (400 when blank), unlike comp/regrant. Extra guards beyond the
ticket: forcing `trial` with an ended trial window → 400 pointing at re-grant (a bare flip
would be lazily re-lapsed by the next verdict — silent no-op); forced statuses clear stale
`grace_until`; change-plan validates the tier is active. Plus `GET /admin/subscriptions/plans`
(reuses `getActivePlans`) for the picker.

**Frontend** (`SubscriptionAdmin.vue`): Support-actions row in the drawer (cancel/uncancel
toggle-label, force status, change plan) → one shared confirm dialog (`append-to-body`) with
target select + required reason (confirm disabled while blank); success refreshes the drawer
detail, audit trail and results.

**Verification:** 16 new unit tests (happy paths + audit actions/reasons, 400s for blank
reason / bad status / unknown+inactive tier / non-boolean cancel, 404s); full backend suite
1412/1413 green (the 1 failure is `rate-limit-logging.test.js`, a pre-existing timeout flake
confirmed failing on the unmodified tree). All four new routes verified live on the dev
server as superadmin-gated (401 unauthenticated). Vue template + script compile clean.
Not verified locally: an authenticated browser end-to-end (needs admin creds); the seam is
unit-covered.

**Review:** angle sweep on the diff — no blocking findings; accepted risks noted (stale
cancel-toggle race between two concurrent admins; force-active ignores past period end by
design — webhooks own timed transitions; same-tier plan change is a harmless audited no-op).
