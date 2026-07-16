# Cheeko Subscription System — Implementation Spec

> Assembled 2026-07-14 from the wayfinder map `docs/wayfinder/subscription-system/` (17 tickets, all decisions
> linked below). Pricing basis: `docs/cheeko-pricing-strategy.md`. Metering basis: `docs/plan-usage-tracking-and-limits.md`.
> Architecture context: `docs/cheeko-system-overview.md`.
> **Design-complete; no code was written.** Implementation tickets: `docs/issues/subscription/` (SUB-1…13).
> On implementation detail, the **issue bodies override this spec** — they carry the newer edge-case work
> (lazy trial expiry, webhook/checkout race, nightly Razorpay reconciliation, `paused` mapping, refund→lapse,
> account-deletion mandate cleanup; full catalog in `docs/cheeko-subscription-backend.html`).

## 0. Decisions summary (with ticket links)

| Decision | Answer | Ticket |
|---|---|---|
| Payment rails | Razorpay Subscriptions (UPI Autopay + cards) | [001](wayfinder/subscription-system/tickets/001-payment-rails.md) |
| Purchase channel | **Web portal `pay.cheekoai.in` for BOTH platforms; no buy button in either app** (Netflix pattern); Android UCB deferred | [017](wayfinder/subscription-system/tickets/017-purchase-channel-confirmation.md), [007](wayfinder/subscription-system/tickets/007-app-store-policy-research.md) |
| Subscription unit | Per device (MAC); parent account pays | [002](wayfinder/subscription-system/tickets/002-subscription-unit.md) |
| Trial | 1 month free, Family-tier limits, once per MAC ever, starts at **first-ever bind**, no card | [003](wayfinder/subscription-system/tickets/003-trial-shape.md), [005](wayfinder/subscription-system/tickets/005-trial-lifecycle-design.md) |
| Packaging | Questions customer-facing (100/300/800/mo), minutes enforced invisibly; billing-anniversary reset; no rollover; IST days | [010](wayfinder/subscription-system/tickets/010-packaging-decision.md) |
| Billing cycle | **Monthly-only at launch** (annual later) | [012](wayfinder/subscription-system/tickets/012-subscription-schema-design.md) |
| Lapse | Gate AI voice + imagine only; RFID/content keep working; 3-day grace on failed renewal (not on trial end) | [004](wayfinder/subscription-system/tickets/004-lapse-behavior.md) |
| Gate notice | Gateway streams pre-recorded Opus clip over existing UDP session; one generic English clip at launch | [009](wayfinder/subscription-system/tickets/009-kid-facing-lapse-experience.md) |
| Enforcement | `session-verdict` at session start, fail-open + alert; graceful finish on bucket-zero; **minute-cap hard cutoff via 5-min heartbeat in v1** | [013](wayfinder/subscription-system/tickets/013-enforcement-extension-design.md) |
| Migration | Fleet <100: every bound device gets a fresh trial seeded at launch day; global kill-switch | [011](wayfinder/subscription-system/tickets/011-field-device-migration.md) |
| Admin | Extend `/admin-dashboard`: view + comp/extend + audited trial re-grant; refunds in Razorpay dashboard; SQL-view metrics; 3 alert types | [015](wayfinder/subscription-system/tickets/015-admin-ops-surface.md) |
| Visual direction | **A · Sunshine** (cream/amber/coral toy-brand); prototype: `wayfinder/subscription-system/research/purchase-ux-prototype.html` | [014](wayfinder/subscription-system/tickets/014-purchase-ux-prototype.md) |
| Pre-launch survey | Skipped — launch data replaces it; instrument kept for post-launch | [008](wayfinder/subscription-system/tickets/008-parent-packaging-research.md) |

Key Razorpay facts constraining everything ([006](wayfinder/subscription-system/tickets/006-razorpay-subscriptions-research.md)): no payment-failure webhook (`pending`→`halted`), grace is merchant-side, **UPI plan change = cancel + re-create with fresh mandate**, trials require card upfront (so ours is app-layer), webhooks at-least-once/unordered/24h-retry.

## 1. Plans

| | Starter ₹199/mo | Family ₹499/mo ⭐ | Premium ₹999/mo |
|---|---|---|---|
| Questions/month | 100 | 300 | 800 |
| Daily question cap | 15 | 40 | 80 |
| Daily minute backstop (invisible) | per pricing-doc §4b mapping (~60 min/mo scale) | ~200 min/mo scale | ~500 min/mo scale |
| AI images | 150/mo | unlimited (25/day) | unlimited |
| Features | all characters | + memory, weekly summary | + 2 kid profiles, deep insights |

Exact daily-minute values are set in the `subscription_plans` seed (start: Starter 8 min/day, Family 15, Premium 30 — pricing doc §4b) and are tunable in DB without deploys. 1 question = 1 user message (`device_token_usage_session.message_count`). Trial = Family limits.

## 2. Data model (manager-api, Prisma — 4 new models)

Full column lists in [ticket 012](wayfinder/subscription-system/tickets/012-subscription-schema-design.md). Summary:

- **`subscription_plans`** — DB-driven catalog: tier, price_inr, all limits, features JSON, `razorpay_plan_id`, is_active.
- **`device_subscriptions`** — spine, **`mac_address` UNIQUE (not FK to `ai_device` — unbind deletes that row, `device.service.js:184`)**: status `trial|active|grace|lapsed|cancelled`, plan_id, user_id (payer, survives unbind), trial fields (`trial_used` permanent), `current_period_start/end` (bucket anchor), `grace_until`, `cancel_at_period_end`, razorpay customer/subscription ids.
- **`subscription_events`** — webhook ledger, unique `razorpay_event_id` (idempotency), payload JSON.
- **`subscription_admin_audit`** — admin_user, action (`comp_extend|trial_regrant|plan_override`), reason, before/after JSON.

**Usage = live SUMs** over `device_token_usage_session` (period window for monthly, IST day for daily). No counter columns at this scale.

### State machine

```
(none) ──first bind──▶ TRIAL(+30d, plan=family)
TRIAL ──expiry──▶ LAPSED          TRIAL ──purchase──▶ ACTIVE
ACTIVE ──sub.pending @renewal──▶ GRACE(+3d)
GRACE ──sub.charged──▶ ACTIVE     GRACE ──expiry/halted──▶ LAPSED
ACTIVE ──cancel──▶ cancel_at_period_end ──period end──▶ CANCELLED
LAPSED|CANCELLED ──purchase──▶ ACTIVE
UPI plan change = cancel Razorpay sub + create new (same row, new razorpay_subscription_id)
```

## 3. API surface (manager-api)

**Gateway-facing (X-Service-Key):**
- `GET /device/:mac/session-verdict` → `{allowed, reason: ok|no_plan|monthly_bucket_empty|daily_questions|daily_minutes|daily_images, remaining:{questions_month, questions_today, minutes_today, images_today}}`. No cache; **fail-open + alert** on error. Called by gateway `_deferredSetup` before LiveKit dispatch AND before the imagine WS handoff.
- `POST /device/:mac/usage-heartbeat` (worker, every 5 min) → updates in-flight session usage; response may carry `{cutoff: true}` when the daily minute cap is breached → gateway sends `end_prompt` (hard "time's up"). This is Phase 1.4 of `plan-usage-tracking-and-limits.md`, **now a launch prerequisite**.

**Portal/app-facing (Firebase Bearer, `/api/mobile`):**
- `GET /api/mobile/subscription/plans` — active catalog.
- `GET /api/mobile/devices/:mac/subscription` — status, plan, period, usage summary, trial countdown.
- `POST /api/mobile/devices/:mac/subscription/checkout` — creates Razorpay customer+subscription, returns checkout params (portal only).
- `POST /api/mobile/devices/:mac/subscription/cancel` — sets cancel_at_period_end.
- `POST /api/mobile/devices/:mac/subscription/change-plan` — orchestrates UPI cancel+recreate flow.

**Razorpay-facing:**
- `POST /webhooks/razorpay` — HMAC-SHA256 verify, insert-or-skip by `razorpay_event_id`, then state transitions (order-tolerant: derive state from subscription status fetch when events arrive out of order). Handles: `authenticated, activated, charged, pending, halted, paused, resumed, cancelled, completed`.

**Admin (`/admin-dashboard`, ADMIN_PASSWORD):**
- Subscription search/view; comp/extend; trial re-grant — all writing `subscription_admin_audit`; metrics page over SQL views (funnel bind→trial→paid, churn, MRR, gate-hit counts).

## 4. Trial lifecycle & notifications

- Trial row created inside `deviceBind` (`device.service.js:81` path) when no `device_subscriptions` row exists for the MAC (`trial_used=false` ⇒ create as `trial`; else create as `lapsed`).
- FCM pushes (existing `parent-profile/fcm-token` registration): day 23 "1 week left", day 27 "3 days — pick a plan", day 30 "trial ended"; **80% bucket** push ("240 of 300 used"); plan-gate pushes (trial-ended / lapsed / bucket-empty — never daily-cap). In-app banner throughout trial.
- Renewal comms: Razorpay sends the mandatory 24h pre-debit notification; we push on `pending` (payment issue → fix link) and `halted`.

## 5. Enforcement flows

**Voice**: hello → gateway fast-path (unchanged) → `_deferredSetup` calls `session-verdict` → refused ⇒ skip LiveKit dispatch, stream gate clip (one generic English clip: "ask Mumma or Papa to check the Cheeko app…") over the already-established UDP session + `tts start/stop`, fire parent push if plan-related → allowed ⇒ dispatch as today. Bucket-zero mid-session: finish gracefully. Minute-cap breach mid-session: heartbeat detects → `end_prompt`.

**Imagine**: same verdict before line_art WS handoff; image counters from the imagine-upload path.

**Music/story**: modes retired — no policy. (Sweep `cheeko-system-overview.md` references during implementation.)

**Kill-switch**: global `ENFORCEMENT_ENABLED` flag (manager-api env/config row) — verdict returns `allowed=true` for all when off.

## 6. Purchase portal (`pay.cheekoai.in`) — new component

Small standalone web app (own repo/deploy). Firebase web SDK, same Google sign-in → same parent identity → manager-api `/api/mobile` with Bearer token. Screens (direction **A · Sunshine**; see prototype): Plans (3 tiers, hero=Family, trial banner), Checkout (Razorpay Subscriptions handoff, UPI Autopay mandate auth, 24h pre-debit reassurance copy), Manage (plan/renewal/mandate state, cancel, change-plan with "fresh mandate approval" explainer). GST-compliant tax invoices are our responsibility — portal serves invoice downloads (implementation detail; Razorpay auto-invoice adequacy UNVERIFIED).

**Parent app changes (Flutter)**: plan/usage view (trial countdown ring, buckets), gate-moment screen — **no purchase CTA**; "Manage at pay.cheekoai.in" wording (iOS-safe); FCM handlers for the new pushes. `flutter_local_notifications` is currently unwired — wire foreground display as part of this.

## 7. Migration & rollout

1. Seed `subscription_plans` (3 rows) + create 3 Razorpay plan objects (test then live).
2. Launch-day script: insert `device_subscriptions` (status=trial, trial_started_at=now) for every currently-bound MAC (<100).
3. Pre-launch comms: in-app announcement + one push ("plans are coming, your first month is free").
4. Flip `ENFORCEMENT_ENABLED`. Watch funnel dashboard + alerts (webhook failures, fail-open events, mandate-halted spikes → email/Slack).

## 8. Phased implementation plan (each phase independently verifiable)

| # | Phase | Where | Verify |
|---|---|---|---|
| 0 | **Prerequisites**: tracking holes 1.1–1.3 + **1.4 heartbeat** (now required), Bulbul v3 TTS swap (margin dependency, parallel track) | picoclaw | per `plan-usage-tracking-and-limits.md` verify steps |
| 1 | Schema + plan seed + state-machine service (pure functions) | manager-api | unit tests: every transition incl. out-of-order webhooks |
| 2 | Trial creation in bind + trial expiry job + `session-verdict` (verdict works before payments exist) | manager-api | bind → trial row; 31-day-old trial → `no_plan`; kill-switch returns allowed |
| 3 | Gateway gating: verdict call in `_deferredSetup` + imagine handoff; gate clip streaming; fail-open path | mqtt-gateway | simulator (`client.py`): lapsed device hears clip, no room created; manager-api down → session allowed + alert |
| 4 | Razorpay: checkout endpoint, webhook handler + ledger, grace logic, cancel/change-plan | manager-api | Razorpay test mode: full lifecycle incl. forced failure → grace → halted → lapsed |
| 5 | Portal (plans/checkout/manage, direction A) | new repo | test-mode purchase end-to-end: trial device → active |
| 6 | Parent app: plan/usage view, gate screen, pushes wired | Flutter app | trial countdown renders; gate push deep-links |
| 7 | Admin: subscription view, comp/extend, trial re-grant + audit, metrics page, alerts | manager-api | override writes audit row; metrics views return |
| 8 | Migration + launch: seed fleet trials, comms, flip flag | all | funnel dashboard live; kill-switch revert tested |

**Post-launch validations** (replacing the skipped survey): trial→paid conversion, bucket-consumption distribution (recheck the 50% breakage assumption), Bulbul GA pricing, watch Google expanded-billing India rollout (UCB revisit trigger).
