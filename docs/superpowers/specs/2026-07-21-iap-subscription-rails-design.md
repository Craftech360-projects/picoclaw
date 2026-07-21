# In-app subscription rails (Apple IAP + Google Play Billing via RevenueCat)

> 2026-07-21. Supersedes the purchase-channel and payment-rails decisions in
> `docs/cheeko-subscription-spec.md` (§0 rows "Payment rails" and "Purchase channel") and
> wayfinder tickets 001/017. Everything else in the spec (unit, trial, packaging, lapse,
> enforcement, migration) stands. Approved by Rahul in brainstorming session 2026-07-21.

## Decision

Parents buy subscriptions **inside the parent app on both iOS and Android** — no web
portal. Because Apple 3.1.1 mandates IAP for digital subscriptions in India (no
external-link entitlement; see `docs/wayfinder/subscription-system/research/app-store-policy.md`),
the rails are **Apple IAP + Google Play Billing**, integrated through **RevenueCat**.

| Decision | Was | Now |
|---|---|---|
| Purchase channel | Web portal `pay.cheekoai.in`, no in-app buy button | **In-app, parent app, both platforms** |
| Payment rails | Razorpay Subscriptions (UPI Autopay) | **Apple IAP + Google Play Billing via RevenueCat** |
| Subscription unit | Per device (MAC) | **Unchanged.** Store-side ceiling accepted: one subscribed device per store account at launch (Apple: one sub per subscription group; Google: one per product). Multi-device parents → admin comp (SUB-11). Documented upgrade path: device-slot products or per-account model. |
| GST invoices | Our responsibility (portal serves downloads) | **Stores are merchant of record — GST off our plate** |
| Trial | App-layer, 1 month at first bind, no card | **Unchanged.** Store-native trials unused; IAP purchase starts the paid plan. |
| Pricing | ₹199 / ₹499 / ₹999 monthly | Same numbers at launch (tunable in store consoles without deploys). Net after 15% store cut: ₹169 / ₹424 / ₹849. **Action: enroll in Apple Small Business Program** (else 30%). |
| Plan change (UPI cancel+recreate) | Orchestrated by manager-api | **Store-native upgrade/downgrade** (Apple subscription group, Google replacement mode) → RevenueCat `PRODUCT_CHANGE` event. |

## Why RevenueCat (vs direct)

Direct = `in_app_purchase` plugin + App Store Server Notifications v2 (JWS verify) +
Google RTDN (GCP Pub/Sub) + both store APIs — two webhook stacks owned forever, roughly
2× the Razorpay integration. RevenueCat collapses that to one SDK + **one normalized
webhook**. Free until $2.5k/mo tracked revenue (~400 Family subs), then 1%. Ceiling
accepted; migration off is possible later.

## Architecture

```
Parent app (Flutter)                          manager-api (Node)
  purchases_flutter                             POST /webhooks/revenuecat
  Purchases.logIn(appUserID = MAC)  ──buy──▶      verify Authorization header (shared secret)
  paywall on plan/usage + gate screens            ledger insert → subscription_events
                                                  (unique event id, dupe ⇒ 200 no-op)
Stores ──receipts / server notifications──▶     map event → existing state machine
                RevenueCat                        → device_subscriptions
        (validates, normalizes, retries)
```

- **`appUserID = MAC`** — the RevenueCat identity IS the device. Entitlement ↔ device 1:1;
  every webhook carries the MAC; no checkout/webhook race exists (there is no server-side
  checkout call — the app buys natively, the webhook lands, done). Parent managing a second
  device: app calls `logIn(<other MAC>)` when switching device context.
- **Event mapping** (state machine from SUB-1..5 untouched):
  `INITIAL_PURCHASE → active` (period = store period), `RENEWAL → active` + roll period,
  `BILLING_ISSUE → grace` (SUB-7), `EXPIRATION → lapsed`, `CANCELLATION → cancel_at_period_end`,
  `UNCANCELLATION → clear it`, `PRODUCT_CHANGE → swap plan_id` (SUB-9). Period anchors only
  move forward, same rule as the Razorpay handler.
- **DB delta**: `device_subscriptions` + `store` (`app_store|play_store`) and
  `rc_original_transaction_id`. Razorpay columns stay, dormant.
- **Store config**: 3 auto-renewable products (starter/family/premium monthly) — one Apple
  subscription group; matching Google subscriptions; 3 RevenueCat entitlements mapped to tier.
- **Enforcement path unchanged**: session-verdict, buckets, heartbeat cutoff, gate clip —
  zero changes.

## Fate of existing work

- **SUB-6 Razorpay backend** (`234bca15` on `Subscription_implemetation`, 24/24 tests):
  stays committed and dormant — wired to nothing, deleted from nothing. It is the fallback
  if IAP economics hurt. Ticket closed as "built, shelved by rails pivot".
- **SUB-8 portal**: superseded, closed. Sunshine-direction UX (wayfinder 014 prototype)
  carries over to the in-app paywall.
- **SUB-7**: re-scoped to RevenueCat events. **SUB-9**: shrinks to `PRODUCT_CHANGE` + UI.
- **New**: SUB-15 (backend webhook), SUB-16 (Flutter purchase UI), SUB-17 (HITL store setup).

## Testing

Unit: RC event → transition tests in the SUB-6 style (no live DB). E2E: store sandbox
purchases (free) → RC sandbox webhook → device flips active → session-verdict allows.
No tunnels or live payment keys needed, unlike the Razorpay e2e that blocked SUB-6.

## Risks / ceilings

- One subscribed device per store account at launch (see table). Admin comp is the valve.
- RevenueCat outage delays webhooks → nightly reconciliation (SUB-7) covers drift via RC REST API.
- 15% margin cut is real: Family nets ₹424 against ~₹2.6/min COGS — revisit pricing with
  launch data (post-launch validations, spec §8).
- Apple review of a kids-hardware companion app selling subscriptions: parent app is the
  purchaser (parental-control category, not Kids), matching the Miko precedent.
