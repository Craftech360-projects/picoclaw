---
id: 17
title: Purchase channel confirmation (amended rails)
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

[App-store policy research](007-app-store-policy-research.md) amended the original [Payment rails](001-payment-rails.md) decision: in-app Razorpay checkout is a high-risk iOS rejection and Razorpay-only is noncompliant on Android today. Confirm the amended channel strategy with the human:

1. **iOS**: web purchase portal, no purchase surface in the app (Netflix pattern; research confidence HIGH). Accept? Funnel: QR/email/WhatsApp link from the parent app's non-transactional screens (policy-permitted wording to be checked at implementation).
2. **Android**: choose launch channel — (a) Razorpay via Play **user-choice billing** (~11% effective fee + alt-billing API work), or (b) same web portal as iOS at launch, UCB later (research suggests this if UCB is too heavy; watch Google "expanded billing" India rollout).
3. If any web portal is chosen: scope it — standalone Next.js/simple page vs part of manager-api admin stack; Firebase login reuse; where it lives (`pay.cheekoai.in`?).
4. Consequence check: does the portal choice change [Purchase UX prototype](014-purchase-ux-prototype.md) (in-app screens become "view + deep-link out" only) and [Schema design](012-subscription-schema-design.md) (no change expected — channel-agnostic)?

## Resolution (2026-07-14, grilling session)

**Web purchase portal for BOTH platforms at launch; no buy button in either app.**

1. **iOS**: Netflix pattern accepted — the app shows plan/usage, purchase happens on the portal. Funnel via QR on the toy box / onboarding, WhatsApp/email links. In-app mention of the portal must use policy-permitted wording (implementation detail, flagged for the spec).
2. **Android**: same portal at launch — one purchase flow, 0% store fees, ~2% Razorpay only, UPI Autopay works fine on web. **UCB (user-choice billing) deferred**: revisit if Play compliance pressure or conversion data demands an in-app path (watch Google "expanded billing" India rollout, per [App-store policy research](007-app-store-policy-research.md)).
3. **Portal shape**: small standalone web app (e.g. `pay.cheekoai.in`) — Firebase web SDK with the same Google sign-in → same parent identity → calls the same manager-api `/api/mobile` endpoints with the Firebase Bearer token that already works. New repo/deploy; zero new auth or API concepts.
4. **Consequences recorded**: [Purchase UX prototype](014-purchase-ux-prototype.md) now covers the PORTAL screens (plans/checkout/manage) plus in-app view-and-deep-link surfaces — question updated. [Schema design](012-subscription-schema-design.md) unchanged (channel-agnostic). The spec ([Write the implementation spec](016-write-implementation-spec.md)) gains the portal as a sixth platform component.
