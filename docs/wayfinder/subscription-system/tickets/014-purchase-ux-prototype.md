---
id: 14
title: Purchase & manage-subscription UX prototype
type: wayfinder:prototype
status: closed
assignee: rahul
blocked-by: [6, 7, 17]
---

## Question

Prototype the subscription UX surfaces (via `/prototype`, throwaway fidelity) so the flows can be reacted to before the spec freezes them. Per [Purchase channel confirmation](017-purchase-channel-confirmation.md), purchase lives on a **web portal** (`pay.cheekoai.in`, Firebase web auth) — both apps are view-and-deep-link only.

**Portal (where money changes hands):**
1. **Plans page** — 3 tiers + annual toggle, hero-plan emphasis, trial-days-remaining banner; Google sign-in.
2. **Checkout** — Razorpay Subscriptions handoff (UPI Autopay mandate auth), success/failure returns.
3. **Manage** — current plan, renewal date, cancel, payment-method state (mandate active/halted); note UPI plan-change = cancel + new mandate (per [Razorpay research](006-razorpay-subscriptions-research.md)).

**Parent app (no buy button — policy-safe wording only):**
4. **Plan/usage view** — current plan, questions/images used this month, trial countdown.
5. **Gate moments** — what the parent sees when the kid hits a gate (push → screen explaining + how to subscribe, without a direct purchase CTA on iOS).

Flutter context: screens under `lib/screens/`, state via `provider`, API via `JavaApiService` patterns (`docs/cheeko-system-overview.md` §6). Prototype output linked from this ticket as an asset.

## Progress (2026-07-14)

**Prototype built and published — awaiting human reaction (HITL).**

- Asset: [purchase-ux-prototype.html](../research/purchase-ux-prototype.html) · live at https://claude.ai/code/artifact/18d5e28b-6fe0-4650-a4f7-d5cf18852fce
- Five surfaces (portal plans / checkout / manage · app plan-usage / gate-moment in a phone frame) × three design directions switchable from a floating bar: **A Sunshine** (toy-brand playful), **B Trust** (fintech clean), **C Bedtime** (warm premium dark).
- Decisions baked in for reaction: annual toggle = pay-10-months, UPI-mandate re-approval notice on plan change, 24h pre-debit reassurance at checkout, "still works without a plan" framing on the gate screen, iOS-safe view-only wording on app screens.
- Resolution needs: chosen direction (or mix), copy/flow corrections, and any screen judged missing — then close with the verdict.

## Resolution (2026-07-14, human reaction)

**Direction A — Sunshine** (toy-brand playful: cream `#FFF8ED` ground, amber `#FFB703`/coral `#FB8500` accents, deep navy `#023047` text, chunky rounded shapes, rounded type). Chosen for both the portal and the app surfaces.

- No copy or flow corrections raised; no screens judged missing — the five prototyped surfaces and their baked-in decisions (annual pay-10-months toggle, UPI mandate re-approval notice, 24h pre-debit reassurance, "still works without a plan" gate framing, iOS view-only wording) stand as reacted-to.
- The prototype asset stays linked as the visual reference for the spec's screen section; it is throwaway fidelity, not a build target.
