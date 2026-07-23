# Subscription admin dashboard — upgrade plan

2026-07-23. Turns the current read-only monitor into a management console.

## Problem

Today's `/subscription-admin` page ([manager-web] `src/views/SubscriptionAdmin.vue`, Vue 2 + Element UI)
is a **monitoring readout**, not a management tool. It shows aggregate metrics, a flat
search, and a recent-audit list — but the only actions are comp-extend and regrant-trial,
and there is **no way to see one device in depth**. An admin fielding "my toy stopped
talking" can't see *why* a device is gated, how close it is to its buckets, what webhook
history it has, or whether a store refund actually landed.

The backend already returns much of the missing data (search payload carries `grace_until`,
`cancel_at_period_end`, `current_period_start`, `trial_started_at`, plan `tier`/`price_inr`,
`parent_phone`) — it's just not rendered. The rest (`store`, `rc_original_transaction_id`,
the `subscription_events` timeline, live usage-vs-limits) exists in the DB but no admin
endpoint exposes it.

## What an admin actually does (design targets)

Per the spec (launch fleet <100, per-account device ceiling with admin comp as the valve,
kill-switch, retro-trials), the real jobs are:

1. **Support triage** — parent reports a problem → look up the device, see status + *why
   gated* + usage + history, then fix it (comp / regrant / uncancel / reactivate). This is
   the workhorse and today's biggest gap.
2. **Multi-device comp** — a parent with 2+ devices hits the one-sub-per-store-account
   ceiling → comp the extra device. (Documented valve; needs a fast lookup + action.)
3. **Refund / billing reconciliation** — a store refund happened → confirm the RevenueCat
   `CANCELLATION` landed and the device lapsed. Today the Refunds card is a static blurb.
4. **Launch monitoring** — watch the funnel, gate-hit reasons, and fail-opens over time.

## Plan (phased, ordered by admin value)

### Phase 1 — Device detail drawer  *(highest value; the support workhorse)*
Click any search/list row → an `el-drawer` showing everything about that MAC:
- **Status & why** — current status, and if gated, the reason (reuse the verdict's bucket
  logic) + which bucket.
- **Plan & limits** — tier, price, and all limits (question/minute/image, daily+monthly).
- **Period & trial** — `current_period_start/end`, `grace_until` with a live countdown,
  `trial_started_at/ends_at`, `trial_used`, `cancel_at_period_end` flag.
- **Store** — `store` (app_store/play_store), `rc_original_transaction_id`.
- **Usage vs limits** — live buckets: questions this month/today, minutes today, images
  today, each as used/limit with a bar (join `device_token_usage_session` +
  `device_image_generations` against the plan, same math the verdict uses).
- **Event timeline** — the `subscription_events` ledger for this MAC (type, time,
  processed_at), newest first — the webhook history.
- **Audit history** — this device's override history (`before_state`→`after_state` diff).

**Backend:** one new endpoint `GET /admin/subscriptions/:mac/detail` that assembles the
above (reuses `subscription.service` usage/verdict helpers so the numbers can't drift from
enforcement). **Frontend:** `el-drawer` opened from the results table.

### Phase 2 — Actions from the drawer
All audited (reason required), all with a confirm step:
- **Cancel / uncancel** — toggle `cancel_at_period_end` (manual store-desync repair).
- **Set-status override** — force `lapsed` / reactivate to `active`/`trial` (support
  escape hatch), written through the same audit trail.
- **Change plan** — re-point `plan_id` (e.g. correct a mis-mapped product).

**Backend:** `POST /admin/subscriptions/:mac/{cancel,status,plan}`, each validating +
writing `subscription_audit` with before/after, mirroring the existing comp/regrant pattern.

### Phase 3 — Refund / billing reconciliation
- Search by `rc_original_transaction_id` (add it to the `search` q-match).
- In the drawer, surface the store CANCELLATION/EXPIRATION event that lapsed the device, so
  a refund can be confirmed in one look. Replaces the static Refunds blurb with a real lookup.

**Backend:** extend `searchSubscriptions` to match the RC txn id; no new endpoint.

### Phase 4 — Metrics upgrade
- **Date-range picker** (default 30d) driving the funnel/churn/gate-hit queries.
- **Trends** — trials / paid / lapsed as small time-series (sparklines) instead of point-in-time only.
- **Per-plan MRR breakdown** (starter/family/premium) instead of one number.
- **Gate-hit drill-down** — click a reason chip → list the devices that hit it (opens the drawer).

**Backend:** time-bucketed variants of `getMetrics`; a `gate-hits?reason=` list endpoint.

### Phase 5 — Audit polish
- Render the `before_state`→`after_state` **diff** (already returned, never shown).
- Filter by admin / action / date; CSV export for the audit trail.

### Quick wins (ship immediately, no backend change)
The search/list payload *already* carries these — just render them in the results table:
grace countdown (`grace_until`), a "cancelling at period end" tag (`cancel_at_period_end`),
plan tier + price (only `name` shown today), parent phone.

## Sequencing & effort

| Phase | Admin value | Backend | Frontend | Suggested order |
|---|---|---|---|---|
| Quick wins | Medium | none | small | **1st** (same PR as Phase 1) |
| 1 Detail drawer | **Highest** | 1 endpoint | 1 drawer | **1st** |
| 2 Actions | High | 3 endpoints | drawer buttons + dialogs | 2nd |
| 3 Refund lookup | Medium-High | extend search | drawer section | 3rd (cheap, pairs with 1) |
| 4 Metrics/trends | Medium | 2 endpoints | charts | 4th |
| 5 Audit polish | Low-Medium | none | diff + export | last |

**Recommended first PR:** Quick wins + Phase 1 detail drawer + Phase 3 refund lookup — one
new read endpoint (`/detail`) + one search tweak + the drawer UI. That alone converts the
page from "monitor" to "usable support console" and unblocks the multi-device-comp and
refund-reconciliation jobs. Phase 2 (write actions) follows once the read view is trusted.

## Risks / notes

- **Reuse verdict/usage helpers** for the detail view's status-reason and usage-vs-limits —
  if the dashboard re-implements bucket math it will drift from what actually gates devices.
- All new write actions go through the **existing audit trail** (never a bare UPDATE) — the
  audit table is the admin's accountability record and the launch runbook leans on it.
- Element UI (Vue 2) is the existing stack; stay on it (no framework churn for this).
- Scope is a **<100-device launch fleet** — no need for server-side pagination/virtualization
  yet; the plain tables are fine. Revisit if the fleet grows past a few hundred.
