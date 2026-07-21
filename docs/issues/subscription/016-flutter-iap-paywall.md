---
id: SUB-16
title: "Parent app: purchases_flutter paywall & purchase flow"
type: AFK
status: open
triage: afk-ready
assignee:
blocked-by: [SUB-15, SUB-17]
---

## Parent

`docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`; UX direction A · Sunshine
(wayfinder 014 prototype: `docs/wayfinder/subscription-system/research/purchase-ux-prototype.html`).
Repo: `D:\Cheeko-mobile_app\CheekoAI-Parent-App`.

## What to build

Add `purchases_flutter`. Configure with `appUserID = <MAC>` of the device being managed;
`logIn(<mac>)` on device-context switch. Screens (Sunshine palette): **Plans/paywall** — 3
tiers from `GET /api/mobile/subscription/plans` merged with RC `Offerings` for store prices,
hero=Family, trial-days banner; reachable from the plan/usage view and the gate screen
(SUB-10 surfaces). **Purchase** — native store sheet via `purchaseStoreProduct`, success ⇒
poll `GET /api/mobile/devices/:mac/subscription` until active (webhook latency), celebratory
state. **Manage** — current plan, renewal date, cancel/change-plan buttons deep-linking to
store-native management (`showManageSubscriptions` / Play subscription URL). Restore-purchases
button (store-review requirement).

No prices hardcoded — always render store-returned localized prices (Apple requirement).

## Acceptance criteria

- [ ] Sandbox purchase on iOS and Android: device flips to active in the app within one poll cycle
- [ ] Paywall renders the 3 tiers with store prices; Family is the hero; works on a device with no plan (lapsed) and in-trial
- [ ] Restore purchases recovers an existing sub after reinstall
- [ ] Cancel deep-links to store management; app reflects `cancel_at_period_end` on next open
- [ ] Second bound device shows the honest ceiling copy (already-subscribed store account) instead of a broken purchase
- [ ] Store review passes on both platforms (no external purchase links, no price steering copy)

## Blocked by

- SUB-15, SUB-17
