---
id: SUB-20
title: "Admin dashboard: metrics date-range, trends, per-plan MRR, gate-hit drill-down"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-18]
---

## Parent

`docs/issues/subscription/subscription-admin-dashboard-plan.md` (Phase 4). Upgrades the
point-in-time metrics into something you can actually watch over time — the launch-day
monitoring surface.

## What to build

- **Date-range picker** (default 30d) driving the funnel / churn / gate-hit queries.
- **Trends** — trials / paid / lapsed as small time-series sparklines, not just current counts.
- **Per-plan MRR breakdown** (starter / family / premium) instead of one aggregate number.
- **Gate-hit drill-down** — click a reason chip → list the devices that hit it in range →
  each opens the SUB-18 detail drawer.

Backend: time-bucketed variants of `getMetrics` (accept a range); a
`GET /admin/subscriptions/gate-hits?reason=&from=&to=` list endpoint. Use the existing
`subscription_gate_hits` table (include its `flow` field, currently dropped).

## Acceptance criteria

- [ ] Date-range picker re-queries funnel/churn/gate-hits for the chosen window
- [ ] Trials/paid/lapsed render as time-series over the range
- [ ] MRR is broken out per plan and sums to the previous single number
- [ ] Clicking a gate-hit reason lists the devices that hit it; each opens the detail drawer
- [ ] Endpoints superadmin-gated; empty range → clean empty state

## Blocked by

- SUB-18
