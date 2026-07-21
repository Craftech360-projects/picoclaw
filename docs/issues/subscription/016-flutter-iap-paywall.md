---
id: SUB-16
title: "Parent app: purchases_flutter paywall & purchase flow"
type: AFK
status: in-review
triage: afk-ready
assignee: claude
blocked-by: [SUB-15, SUB-17]
---

> **2026-07-21 — implementation complete, pending live e2e.** Branch
> `feat/iap-subscription` (app repo), commits `d4936a4..b32f8a5` (7 task commits + 3
> review-fix commits), pushed. Plan:
> `docs/superpowers/plans/2026-07-21-sub16-flutter-iap-paywall.md` (subagent-driven, per-task
> reviews + final whole-branch review READY-TO-MERGE; final review caught and fixed a
> critical identity bug — purchases now pin RC appUserID to the screen's device MAC via
> `ensureIdentity(mac)` before every store sheet, plus a `kReleaseMode` guard refusing
> `test_` keys). Backend contract additions (plans `store_product_id`, summary
> `cancel_at_period_end`) shipped + deployed to otadev (`7cf2e4c7`).
> App tests: 16/16 subscription-related green; analyze baseline unchanged.
>
> **Remaining before close (needs a human + device/emulator):** run the app against
> otadev, Profile → Subscription: 3 tiers with Test Store prices (Family "Most popular"),
> buy → store sheet → "Confirming with the store…" → celebration within ~one poll;
> backend log shows the INITIAL_PURCHASE applied; re-enter → manage view; Restore works.
> Store-sandbox iOS/Android + store-review criteria stay deferred to SUB-17 verification.

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
