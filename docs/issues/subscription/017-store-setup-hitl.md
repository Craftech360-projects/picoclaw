---
id: SUB-17
title: "Store & RevenueCat account setup (HITL)"
type: HITL
status: open
triage: needs-human
assignee: rahul
blocked-by: []
---

## Parent

`docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`. Human-only console work
that gates SUB-16 (and sandbox e2e for SUB-15).

## Checklist

- [ ] App Store Connect: Paid Apps agreement active; India banking + tax forms complete
- [ ] **Apple Small Business Program enrollment** (15% instead of 30%)
- [ ] App Store Connect: one subscription group, 3 auto-renewable products — `cheeko_starter_monthly` ₹199, `cheeko_family_monthly` ₹499, `cheeko_premium_monthly` ₹999
- [ ] Play Console: payments profile for India; 3 matching subscription products (same ids)
- [ ] RevenueCat: project with both store apps connected (App Store Connect API key, Play service-account JSON), 3 entitlements `starter|family|premium` mapped to the products, offering `default` with 3 packages
- [ ] RevenueCat webhook pointed at manager-api `/webhooks/revenuecat` with the Authorization secret; secret handed to backend env (`REVENUECAT_WEBHOOK_AUTH`)
- [ ] Sandbox testers: one Apple sandbox account + one Play license tester registered
- [ ] Product ids written into `subscription_plans.store_product_id` (SUB-15 seed)

## Progress (2026-07-21)

Both store accounts applied for; **pending verification** (days). To unblock SUB-16 and
webhook e2e meanwhile, the RevenueCat **Test Store** is in use:

- Test Store public SDK key: `test_JmxvpBvcCpfBKGfGVJvfFtZFkMY` (dev only — must never
  ship in a release build; real per-platform keys replace it at launch)
- Still to confirm on the Test Store: 3 products with the exact ids above, entitlements
  `starter|family|premium` attached, offering `default`, webhook →
  `/webhooks/revenuecat` + `REVENUECAT_WEBHOOK_AUTH` in backend env

**Dev backend deployed (2026-07-21):** branch `deploy/otadev-subscription` (= server's
`feat/tts-providers-sarvam-edge-azure` + `Subscription_implemetation`, merge `24c275ee`)
live on the DO dev box (otadev.cheekoai.in, manager-api pm2). All pending migrations
applied (incl. both SUB-15 ones; server has `SKIP_DB_SYNC=1`, so migrations are manual:
`set -a; . ./.env; set +a; npx prisma migrate deploy` — needs the new
`prisma.config.js`). Seed verified: all 3 `store_product_id`s present. Webhook verified
end-to-end from outside: 401 bad auth / 400 no id / processed / duplicate-dedupe; smoke
rows cleaned. RC dashboard webhook values: URL
`https://otadev.cheekoai.in/webhooks/revenuecat`, Authorization = `REVENUECAT_WEBHOOK_AUTH`
in the server's `.env`.

**RC console wiring confirmed (2026-07-21, later):** Test Store products/entitlements/
offering `default` created; `REVENUECAT_API_KEY` (v1 secret key) installed on the dev box
and verified against the RC API (201); RC dashboard webhook configured and its test event
delivered 200 `{"code":0,"msg":"ledgered"}` — the full RC→backend rail is proven.
Remaining SUB-17 items are real-store only (account verification, Small Business Program,
store products, sandbox testers).
