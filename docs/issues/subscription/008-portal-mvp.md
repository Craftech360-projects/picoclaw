---
id: SUB-8
title: "Purchase portal MVP (pay.cheekoai.in)"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-6]
---

## Parent

Spec §6; wayfinder tickets 017 (channel) + 014 (UX, direction A — prototype: `docs/wayfinder/subscription-system/research/purchase-ux-prototype.html`).

## What to build

The one place money changes hands, for both platforms (neither app gets a buy button — store policy). New small web app: Firebase web SDK with the same Google sign-in as the parent app → same identity → calls manager-api `/api/mobile` with the Firebase Bearer token. Three screens in the Sunshine direction (cream/amber/coral, hero=Family): **Plans** (3 tiers, trial-days banner, monthly only — no annual toggle), **Checkout** (Razorpay JS sheet handoff, UPI Autopay mandate approval, 24h pre-debit reassurance copy, GST-inclusive pricing), **Manage** (current plan, usage, renewal date, mandate state, cancel).

Device selection: the signed-in parent's devices come from the existing devices endpoint; purchasing targets one MAC.

## Acceptance criteria

- [ ] Google sign-in yields the same user identity as the mobile app (verified against a shared test account)
- [ ] Full test-mode purchase from the plans page activates the selected device
- [ ] Manage shows live plan/usage/renewal and cancel works (period-end semantics)
- [ ] Mandate-halted state surfaces on Manage with fix-payment path
- [ ] Screens match the Sunshine direction; responsive at phone widths (parents will open this from WhatsApp)
- [ ] No purchase deep-link required from the apps — portal is reachable and usable standalone

## Blocked by

- SUB-6
