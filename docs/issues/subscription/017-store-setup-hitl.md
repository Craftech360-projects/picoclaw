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
