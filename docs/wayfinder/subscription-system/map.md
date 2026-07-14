# Wayfinder Map — Cheeko Subscription System ✅ COMPLETE

> label: `wayfinder:map` · created 2026-07-14 · **destination reached 2026-07-14** — all 17 tickets closed (16 resolved, 1 out of scope).
> Deliverable: [`docs/cheeko-subscription-spec.md`](../../cheeko-subscription-spec.md). Tracker: local markdown (tickets in `./tickets/`, research assets in `./research/`).

## Destination

A full implementation spec for the Cheeko subscription system — DB schema, manager-api endpoints, Razorpay integration, trial logic, enforcement flow, parent-app screens, and a phased implementation plan — detailed enough that build sessions can execute it without re-deciding anything. **No code in this effort.**

## Notes

- Domain docs: `docs/cheeko-pricing-strategy.md` (plans/prices/COGS), `docs/plan-usage-tracking-and-limits.md` (metering + Phase 2 enforcement base), `docs/cheeko-system-overview.md` (cross-repo architecture, §1 manager-api, §6 parent app).
- Skills to consult: `pricing` (tier structure, Van Westendorp), `grill-me` for grilling tickets, `prototype` for UX tickets.
- Standing constraints: India-first, prices in ₹; kid-facing failure modes must stay friendly; per-device metering via `device_token_usage_session` is the only trusted meter.
- Launch prerequisites tracked OUTSIDE this map: Bulbul v3 TTS swap, tracking holes 1.1–1.3, Phase 2 base daily-cap enforcement (see Out of scope).

## Decisions so far

- [Payment rails](tickets/001-payment-rails.md) — Razorpay Subscriptions (UPI Autopay + cards), web/in-app checkout; accept Apple-review risk with hardware-service argument.
- [Subscription unit](tickets/002-subscription-unit.md) — per device (one toy = one subscription); parent account is the payer.
- [Trial shape](tickets/003-trial-shape.md) — 1 month free from first-ever activation, at Family-tier limits, granted once per device MAC, ever.
- [Lapse behavior & grace](tickets/004-lapse-behavior.md) — no active plan ⇒ gate AI voice + imagine only (RFID/content/playback keep working); 3-day grace after failed renewal.
- [Razorpay Subscriptions research](tickets/006-razorpay-subscriptions-research.md) — plans immutable, UPI mandates fit our prices; **UPI plan-change = cancel+recreate**; no payment-failure webhook (`pending`→`halted`); grace + trial + GST invoices are merchant-side.
- [App-store policy research](tickets/007-app-store-policy-research.md) — in-app Razorpay is high-risk on iOS and noncompliant alone on Android; verdict: iOS web portal (Netflix pattern), Android user-choice billing or portal. Amended the rails decision.
- [Purchase channel confirmation](tickets/017-purchase-channel-confirmation.md) — web portal (`pay.cheekoai.in`) for BOTH platforms at launch; no buy button in either app; standalone small web app with Firebase web auth reusing manager-api `/api/mobile`; Android UCB deferred.
- [Trial lifecycle design](tickets/005-trial-lifecycle-design.md) — trial starts at first-ever bind (MAC-keyed table surviving unbind — `ai_device` rows get deleted); hard gate at expiry (grace is renewals-only); banner + pushes day 23/27/30; no Razorpay/card during trial.
- [Kid-facing lapse experience](tickets/009-kid-facing-lapse-experience.md) — gateway streams a pre-recorded Opus clip over the existing UDP session (no LiveKit, no TTS cost); one generic "ask your parents" message, English-only clip at launch; parent push on plan-related gates only (never daily-cap).
- [Field-device migration](tickets/011-field-device-migration.md) — fleet <100; every field device gets a fresh 1-month trial seeded at launch day (one code path); global enforcement kill-switch, no cohort staging; in-app + push comms before the flip.
- [Admin & ops surface](tickets/015-admin-ops-surface.md) — extend the existing `/admin-dashboard`: view + comp/extend + audited trial re-grant; refunds stay in Razorpay's dashboard (policy documented); SQL-view metrics page (funnel/churn/MRR); alerts on webhook failures, fail-open events, mandate-halted spikes.
- [Purchase & manage-subscription UX prototype](tickets/014-purchase-ux-prototype.md) — direction **A · Sunshine** chosen (cream/amber/coral toy-brand look) for portal + app; prototyped flows and copy stand; asset linked as the spec's visual reference.
- [Packaging decision](tickets/010-packaging-decision.md) — questions customer-facing (100/300/800/mo) with invisible minute backstop; bucket resets on billing anniversary; no rollover; IST days; validation moved to launch data.
- [Subscription schema design](tickets/012-subscription-schema-design.md) — 4 models (`subscription_plans` catalog, MAC-keyed `device_subscriptions` spine, idempotent `subscription_events` ledger, admin audit); live SUM usage (no counters); full state machine; monthly-only at launch; Phase 2 §2.1 subsumed.
- [Enforcement extension design](tickets/013-enforcement-extension-design.md) — `session-verdict` endpoint, fail-open + alert; question bucket start-gated with graceful finish; **minute-cap hard cutoff via 5-min heartbeat ships in v1** (Phase 1.4 now a launch prerequisite); imagine gated by same verdict; music/story modes retired (no policy); one 80% usage push.
- [Write the implementation spec](tickets/016-write-implementation-spec.md) — **destination reached**: `docs/cheeko-subscription-spec.md` assembles everything; 8-phase plan ready for `/to-issues`.

## Not yet specified

- **GST/invoicing compliance detail** — Razorpay research confirmed GST tax invoices are our responsibility (its auto-invoices UNVERIFIED as compliant); the how (generation, delivery to parents) sharpens once the purchase channel is confirmed.
- **Dunning & notification copy/timeline** — exact push/email sequence for trial-ending, renewal-failed, bucket-80%-used; sharpens after trial lifecycle + enforcement design.

## Out of scope

- **Bulbul v3 TTS swap** — launch prerequisite, tracked in `docs/cheeko-pricing-strategy.md` §6; without it plans lose money, but it is cost-stack work, not subscription design.
- **Tracking holes 1.1–1.3 + Phase 2 base enforcement** — prerequisite engineering, tracked in `docs/plan-usage-tracking-and-limits.md`. This map's enforcement ticket EXTENDS Phase 2 (subscription state + monthly buckets); the base daily-cap landing stays there.
- **LLM cost A/B (gemma-4-31b-it)** — cost optimization, `docs/cheeko-pricing-strategy.md` §7.
- **International pricing / multi-currency** — India-first launch; redraw the destination if this returns.
- **Coupons, referrals, gifting** — not part of v1 subscription design.
- **Pre-launch parent pricing survey** — [Parent pricing & packaging research](tickets/008-parent-packaging-research.md) closed unresolved: at <100 devices, launch (trial→paid conversion + usage data) is the better price test; instrument kept for post-launch use.
