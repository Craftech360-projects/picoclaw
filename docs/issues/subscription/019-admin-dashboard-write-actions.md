---
id: SUB-19
title: "Admin dashboard: write actions (cancel/uncancel, set-status, change-plan)"
type: AFK
status: open
triage: afk-ready
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

- [ ] Cancel sets `cancel_at_period_end=true`; uncancel clears it; both audited with reason
- [ ] Set-status override changes status and writes before/after audit; guarded by confirm
- [ ] Change-plan re-points `plan_id`; next verdict applies the new plan's limits
- [ ] Every action requires a reason and appears in the audit trail (and the drawer's history)
- [ ] All three endpoints are superadmin-gated; invalid inputs → 400, unknown MAC → 404

## Blocked by

- SUB-18
