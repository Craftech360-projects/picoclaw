---
id: SUB-21
title: "Admin dashboard: audit diff view, filters & CSV export"
type: AFK
status: open
triage: afk-ready
blocked-by: []
---

## Parent

`docs/issues/subscription/subscription-admin-dashboard-plan.md` (Phase 5). Makes the audit
trail actually reviewable. Standalone — no dependency on the drawer.

## What to build

- **Before/after diff** — the audit endpoint already returns `before_state` and `after_state`
  (JSON slices) but the UI never renders them; show a field-level diff per audit row.
- **Filters** — by admin, action type, and date range (the endpoint already takes `mac`/`limit`;
  add admin/action/date filtering).
- **CSV export** of the filtered audit trail.

Backend: extend `getAuditLog` to accept admin/action/date filters; a small export path (or
client-side CSV from the fetched rows for the <100-device fleet scale).

## Acceptance criteria

- [ ] Each audit row can expand to a before→after field diff
- [ ] Audit list filterable by admin, action, and date range
- [ ] Filtered audit trail exports to CSV
- [ ] Endpoint stays superadmin-gated

## Blocked by

- (none)
