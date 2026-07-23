---
id: SUB-20
title: "Admin dashboard: metrics date-range, trends, per-plan MRR, gate-hit drill-down"
type: AFK
status: closed
triage: afk-ready
assignee: claude
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

- [x] Date-range picker re-queries funnel/churn/gate-hits for the chosen window
      (funnel counts are point-in-time by nature; churn/gate-hits/trends are ranged)
- [x] Trials/paid/lapsed render as time-series over the range
- [x] MRR is broken out per plan and sums to the previous single number (test-asserted)
- [x] Clicking a gate-hit reason lists the devices that hit it; each opens the detail drawer
- [x] Endpoints superadmin-gated; empty range → clean empty state

## Blocked by

- SUB-18

## Resolution

Shipped in `cheeko-backend@8c54c162` (branch `deploy/otadev-subscription`).

**Backend:** `getMetrics({from,to})` — default 30d, clamped to 366d, inverted ranges fall
back to 30d-before-`to`. Response adds `range`, `mrr_by_tier` (sums to `mrr_inr`), and
`trends` (daily buckets); `churn_30d`/`gate_hits_30d` renamed to `churn`/`gate_hits`
(no other consumers — grep-verified). **Design note:** historical per-day *status* is not
stored, so trends are daily counts of the recorded signals — trial starts
(`trial_started_at`), paid events (INITIAL_PURCHASE/RENEWAL), lapses
(EXPIRATION/CANCELLATION) — in UTC day buckets. New
`GET /admin/subscriptions/gate-hits?reason=&from=&to=` groups the ledger per device+flow
(surfacing the previously-dropped `flow` column) with hit count, last hit, parent join;
reason required → 400; empty range → `{devices: []}`.

**Frontend:** header date-range picker driving `loadMetrics`; MRR-by-plan chips; three
inline-SVG sparklines (no chart dependency — none exists in manager-web); gate-hit chips
click → drill-down dialog → row click opens the SUB-18 drawer with the picker's range
carried through.

**Verification:** 6 new unit tests (ranged churn/trends bucketing day-by-day, per-plan MRR
sum property, custom range passthrough, inverted-range clamp, gate-hit grouping/parent
join/400/empty state); subscription suites 111/111 green; Vue template + script compile
clean; both endpoints verified 401-gated on the live dev server. Not verified locally:
authenticated browser pass (no admin creds); the pre-existing `rate-limit-logging` flake
is unrelated. Review sweep: no blocking findings; UTC (not IST) trend buckets accepted and
documented — monitoring granularity, not billing math.
