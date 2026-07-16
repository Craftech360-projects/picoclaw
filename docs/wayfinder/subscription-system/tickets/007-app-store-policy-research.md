---
id: 7
title: App-store policy risk research
type: wayfinder:research
status: closed
assignee: research-agent
blocked-by: []
---

## Question

How risky is shipping Razorpay checkout inside the parent app, and what is the mitigation strategy?

1. **Apple App Store**: current guideline treatment (3.1.x) of subscriptions for services consumed on external hardware (companion apps). Precedents: how do hardware-companion apps with external billing structure their iOS purchase flows in 2025–26? Reader-app / external-link entitlements applicability in India.
2. **Google Play**: India-specific user-choice billing status; whether a hardware-service subscription must use Play Billing at all.
3. **Fallback shapes** if in-app Razorpay is rejected on iOS: web portal purchase (no purchase button in iOS app, Netflix pattern), external-link entitlement, or iOS-only IAP tier. What each costs us.
4. Recommendation with confidence level.

Findings go to `../research/app-store-policy.md`; link from this ticket on resolution.

## Resolution (2026-07-14, research agent)

Full findings: [research/app-store-policy.md](../research/app-store-policy.md). Key facts:

1. **iOS in-app Razorpay = high-risk rejection.** Apple 3.1.1 mandates IAP for digital subscriptions; the 3.1.3(e) "consumed outside the app" carve-out targets *physical* services and only weakly covers an AI service on a toy. India storefront has **no external-link entitlement path**; anti-steering fully applies. CCI case has no remedies in force.
2. **Precedents**: Miko (closest comparable — Indian kids' AI robot) sells via Apple IAP; Nest Aware, Ring, Whoop all push purchase to the web with zero iOS purchase surface. Nobody ships third-party checkout inside an iOS app.
3. **Android**: the subscription counts as a digital service, so Play Billing applies — but India's **user-choice billing** lets Razorpay sit alongside it (~11% effective fee + alt-billing API work). Razorpay-*only* in the Android app is noncompliant today; Google's fee-free "expanded billing" isn't in India yet.
4. **Recommendation (HIGH confidence iOS, MEDIUM Android)**: **iOS = Netflix-pattern web portal** (no purchase surface in app); **Android = Razorpay via Play user-choice billing, or web portal at launch** if UCB integration is too heavy.

⚠️ This **amends** [Payment rails](001-payment-rails.md)'s "in-app webview" assumption — a web purchase portal is now on the critical path. Human to confirm the amended rails next session (flagged on the map).
