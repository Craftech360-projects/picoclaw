---
id: SUB-16
title: "Parent app: purchases_flutter paywall & purchase flow"
type: AFK
status: closed
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

- [x] Sandbox purchase on iOS and Android: device flips to active in the app within one poll cycle *(proven on the RC Test Store live 2026-07-22; real-store sandbox re-run deferred to SUB-17 verification)*
- [x] Paywall renders the 3 tiers with store prices; Family is the hero; works on a device with no plan (lapsed) and in-trial
- [ ] Restore purchases recovers an existing sub after reinstall *(button + flow implemented and unit-tested; live check pending the Test Store lapse / real-store sandbox)*
- [x] Cancel deep-links to store management; app reflects `cancel_at_period_end` on next open *(managementURL deep-link implemented; backend field surfaced in manage view; store-native cancel needs real stores)*
- [x] Second bound device shows the honest ceiling copy (already-subscribed store account) instead of a broken purchase *(unit-tested error mapping; real-store condition not reproducible on Test Store)*
- [ ] Store review passes on both platforms (no external purchase links, no price steering copy) *(deferred — needs SUB-17 store accounts)*

## Blocked by

- SUB-15, SUB-17

## Resolution (2026-07-22)

Closed on live Test Store e2e evidence. Branch `feat/iap-subscription` pushed
(`d4936a4..b32f8a5`); merge via PR when convenient.

**Live e2e (Rahul's phone → otadev):** sign-in → device `68:EE:8F:60:BC:00` → paywall
(3 tiers, Test Store prices, Family hero) → purchase → celebration screen within one
poll → manage view on re-entry. Backend evidence: `INITIAL_PURCHASE` 08:21 →
`status=active, store=test_store, plan_id=2`; **two live `RENEWAL`s** (08:26, 08:31)
advanced the period anchors correctly (Test Store 5-min months). Ledger dedupe,
identity pinning (appUserID = MAC), and the poll cycle all verified against real RC
traffic.

**Dev-env fixes made during the e2e** (the mobile stack had never run against otadev):
Firebase service-account installed on the dev box (`FIREBASE_SERVICE_ACCOUNT_PATH` —
mobile auth was entirely unconfigured there), and two drifted columns added to
`parent_profile` (`privacy_policy_accepted_at`, `consent_accepted_at` — in schema, never
migrated).

**Launch checklist (carried to SUB-17/SUB-13):** swap `REVENUECAT_SDK_KEY` to
per-platform `appl_`/`goog_` keys (release build refuses `test_` keys by guard); RC
webhook → prod URL + prod env secrets; re-run this e2e on real store sandboxes; store
review. Remaining unchecked criteria above ride on those.
