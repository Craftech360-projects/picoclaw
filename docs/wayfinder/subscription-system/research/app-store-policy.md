# App-Store Policy Risk: Razorpay External Billing for Cheeko Subscriptions

**Date:** 2026-07-14
**Resolves:** wayfinder ticket 007 (`../tickets/007-app-store-policy-research.md`)
**Scope:** Flutter parent app (iOS + Android, India storefronts) selling ₹199–999/mo subscriptions for an AI service consumed on the Cheeko toy, via Razorpay checkout instead of store IAP.

---

## 1. Apple App Store

### 1.1 What the current guidelines actually say

Current App Review Guidelines (fetched 2026-07-14 from [developer.apple.com/app-store/review/guidelines](https://developer.apple.com/app-store/review/guidelines/)):

- **3.1.1 (In-App Purchase):** "If you want to unlock features or functionality within your app (by way of example: subscriptions, in-game currencies, game levels, access to premium content, or unlocking a full version), you must use in-app purchase."
- **3.1.3 preamble (Other Purchase Methods):** the listed app categories "may use purchase methods other than in-app purchase," but "cannot, within the app, encourage users to use a purchasing method other than in-app purchase, except for apps on the United States storefront."
- **3.1.3(e) (Goods and Services Outside of the App):** "If your app enables people to purchase physical goods or services that will be consumed outside of the app, you must use purchase methods other than in-app purchase… such as Apple Pay or traditional credit card entry."
- **3.1.3(f) (Free Stand-alone Apps):** "Free apps acting as a stand-alone companion to a paid web based tool (i.e. VoIP, Cloud Storage, Email Services, Web Hosting) do not need to use in-app purchase, provided there is no purchasing inside the app, or calls to action for purchase outside of the app."
- **3.1.4 (Hardware-Specific Content):** "In limited circumstances, such as when features are dependent upon specific hardware to function, the app may unlock that functionality without using in-app purchase (e.g. an astronomy app that adds features when synced with a telescope). App features that work in combination with an approved physical product (such as a toy) on an *optional* basis may unlock functionality without using in-app purchase, provided that an in-app purchase option is available as well."

### 1.2 Does the 3.1.3(e) carve-out cover an AI service running on a physical toy?

**Weak-to-moderate, and not something to bet the launch on.** The carve-out is written for *physical* goods and services (Apple's canonical examples elsewhere: ride-hailing, food delivery, retail). Cheeko's subscription is a **digital/cloud AI service** whose delivery surface happens to be hardware. Two readings:

- *Favorable reading:* the service is 100% consumed on the toy, 0% in the app; the app is pure account/config plumbing. 3.1.3(e) ("services that will be consumed outside of the app") plus the hardware-dependency logic of 3.1.4 ("features dependent upon specific hardware to function... may unlock that functionality without using in-app purchase") gives a colorable argument that Razorpay in-app is permitted.
- *Apple-reviewer reading:* a subscription to conversational AI is a digital service, and 3.1.4's toy example still says "provided that an in-app purchase option is available as well" for optional hardware pairings. Reviewers historically classify cloud-delivered digital services as IAP-mandatory regardless of the consumption device. **UNVERIFIED:** there is no published Apple adjudication of an "AI service on hardware" case either way; this is inference from guideline text and observed market behavior.

**Verdict: taking payment via Razorpay inside the iOS app is a high-risk rejection under 3.1.1, with an argument (not a guarantee) via 3.1.3(e)/3.1.4.** Apple's review outcomes for edge cases like this are inconsistent, and an appeal loop can burn weeks near launch.

### 1.3 Precedents — how hardware-companion apps actually sell subscriptions on iOS (2024–2026)

| Product | Category | iOS purchase flow |
|---|---|---|
| **Miko (India — kids AI robot, closest comparable)** | Kids robot + "Miko Max" sub | **Uses Apple IAP on iOS.** The [App Store listing](https://apps.apple.com/us/app/miko-play-learn-connect/id1588895826) shows an In-App Purchases section: "Miko Max Yearly $99.00, Miko MAX Monthly $15.00," charged to Apple ID as auto-renewable subscriptions. Note Miko Max also unlocks content *in the app* ("premium content… on the Miko App and Miko robots"), which forces IAP under 3.1.1. Their help docs describe a credit-card flow ("enter your credit card details") which likely applies to Android/web (**UNVERIFIED** — help article was 403 at fetch time; inferred from search snippets of [help.miko.ai](https://help.miko.ai/hc/en-us/articles/23964687347485-Miko-MAX-Subscription-Offers-and-Activation-Process)). |
| **Nest Aware / Google Home Premium** | Camera subscription | **No iOS in-app purchase.** iOS users are directed to the Google Store on the web; only Android users can buy in the Google Home app ([Google support](https://support.google.com/product-documentation/answer/12657252)). |
| **Ring Protect** | Camera subscription | **Web purchase** at [ring.com/plans](https://ring.com/plans); Ring docs direct users to the website rather than the iOS app. |
| **Whoop** | Wearable + membership | Membership managed/renewed at **app.whoop.com** (web) per [Whoop support](https://support.whoop.com/s/article/Updating-Payment-Information); hardware+membership bundles sold on whoop.com. **UNVERIFIED** whether the current iOS app also offers any IAP upgrade path. |
| **Oura** | Ring + membership | Ring bought once on web; membership ~$6/mo. Purchase-channel split iOS-IAP vs web **UNVERIFIED** from sources reviewed. |

Pattern: **no major hardware-companion app ships a third-party card checkout inside the iOS app.** They either use Apple IAP (Miko) or push purchase entirely to the web with no in-app purchase button (Nest, Ring, Whoop).

### 1.4 External-link entitlements / anti-steering in India

- Apple's **StoreKit External Purchase Link Entitlement** exists only for **specific storefronts — the EU (plus lineage from the Netherlands dating-app and South Korea regimes)**; the US needs no entitlement after *Epic v. Apple* ([guidelines 3.1.1(a)](https://developer.apple.com/app-store/review/guidelines/); [Apple entitlement docs](https://developer.apple.com/documentation/BundleResources/Entitlements/com.apple.developer.storekit.custom-purchase-link.allowed-regions); [RevenueCat overview](https://www.revenuecat.com/blog/engineering/app-to-web-purchase-guidelines)). **India is not an eligible storefront.** Guideline text: "In all other storefronts, except for the United States storefront… apps and their metadata may not include buttons, external links, or other calls to action that direct customers to purchasing mechanisms other than in-app purchase."
- The US carve-out comes from the 2025 *Epic v. Apple* contempt ruling; the Ninth Circuit (Dec 2025) upheld contempt but allowed Apple to eventually charge a cost-based fee, and Apple has a **pending US Supreme Court appeal (2026)** while keeping 0% on US link-outs ([MacRumors](https://www.macrumors.com/2025/12/11/apple-app-store-fees-external-payment-links/), [9to5Mac](https://9to5mac.com/2026/05/21/apple-seeks-supreme-court-review-of-contempt-finding-and-injunction-scope-in-epic-games-case/)). **None of this applies to the India storefront.**
- **India CCI case against Apple:** CCI's investigation (from the 2021 TWFS complaint) found in 2024 that Apple abused dominance in iOS app distribution (IAP mandate + anti-steering); the case is still live — Delhi High Court kept the probe alive and in **May 2026 directed Apple to cooperate**; no remedies are in force yet ([MacRumors](https://www.macrumors.com/2026/05/19/india-refuses-to-let-apple-pause-app-store-case/), [Business Standard](https://www.business-standard.com/technology/tech-news/apple-vs-cci-india-s-antitrust-case-reaches-key-stage-on-app-store-rules-126042700411_1.html), [Medianama](https://www.medianama.com/2026/01/223-indias-competition-regulator-apple-antitrust-case-non-cooperation/)). **Do not plan around an India remedy landing before Cheeko ships.**

---

## 2. Google Play

### 2.1 Payments policy — must a hardware-service subscription use Play Billing?

- [Play Payments policy](https://support.google.com/googleplay/android-developer/answer/9858738): Play's billing system is required for in-app purchases of digital goods/services, "including… subscriptions, app functionality, and cloud services," and "apps may not lead users to a payment method other than Google Play's billing system" outside the exemptions.
- Exemptions cover only **physical goods** and **physical services** ("transportation services, cleaning services, airfare, gym memberships"), P2P payments, auctions, donations. An AI conversation service delivered on a toy is a **digital service** — the physical-service exemption does not plausibly apply. **Conclusion: yes, in-policy terms the Cheeko subscription is Play-Billing-scoped.** (**UNVERIFIED** edge: no published Google adjudication for hardware-consumed digital services; same inference caveat as Apple's.)

### 2.2 India status — user-choice billing (UCB)

- Since the CCI's Oct 2022 order (₹936cr fine + mandate to allow third-party billing), Google offers **alternative billing alongside Play billing** ("user choice billing") to **all developers for users in India**: "If a user pays through an alternative billing system, the Google Play service fee will be reduced by 4%" ([Play Console Help — India billing changes](https://support.google.com/googleplay/android-developer/answer/13306652), [UCB overview](https://support.google.com/googleplay/android-developer/answer/13821247)).
- Practical shape: enroll in the program, certify **PCI DSS**, integrate the **alternative billing APIs** (report transactions within 24h), and present the Google-mandated choice screen — user picks Play billing or Razorpay per purchase. Effective Google fee on Razorpay-routed subscription transactions: **15% − 4% = ~11%** (under $1M/yr tier), plus Razorpay's ~2%.
- **Razorpay-only (no Play billing option) in the Android app is NOT compliant today.** Google's June 2026 "expanded billing choice" — alternative billing **without** Play billing as a concurrent option, external purchase links, 5% billing fee removed — launched only in the **US, EEA and UK** effective June 30, 2026, with a staggered global rollout promised but **India not yet included** ([Android Developers Blog](https://android-developers.googleblog.com/2026/06/play-expanded-billing.html)). Watch this: if it reaches India, Razorpay-only in-app becomes compliant with only a ~10% service fee.
- Litigation backdrop: NCLAT (Mar 2025) partly upheld the CCI order and cut the fine to ₹216.69cr; **cross-appeals by Google, CCI and ADIF are admitted at the Supreme Court (Aug 2025, pending)** ([Business Standard](https://www.business-standard.com/industry/news/google-cci-told-to-respond-to-firms-challenging-play-store-billing-policy-124051000923_1.html)). Enforcement risk of skipping UCB and shipping raw Razorpay: Google delisted noncompliant Indian apps in March 2024, so enforcement is real, not theoretical. (**UNVERIFIED:** exact current enforcement posture in 2026.)
- Also note: by Aug 31, 2026 all app updates must use **Play Billing Library v8+** ([Play Console Help](https://support.google.com/googleplay/android-developer/answer/13306652)) — relevant if we integrate UCB.

---

## 3. iOS fallback shapes if in-app Razorpay is rejected

| Shape | Policy basis | Cost / complexity | Assessment |
|---|---|---|---|
| **(a) Web-portal purchase, NO purchase button or CTA in the iOS app (Netflix/Nest/Ring pattern)** | 3.1.3(f) free companion app + 3.1.3(b) accessing subscriptions acquired elsewhere; anti-steering satisfied by having *zero* in-app purchase mentions/links. Proven at scale by Nest Aware, Ring Protect, Whoop. | **Low.** We need a Razorpay-powered web portal anyway (ticket 006). Incremental cost: entitlement sync (already required), careful App Store review hygiene (no "subscribe" copy, no links, no pricing in-app). Main cost is **conversion friction** — parents must discover the portal via email/WhatsApp/packaging/QR-on-box (out-of-app comms are explicitly allowed: "Developers can send communications outside of the app to their user base"). | **Recommended.** Lowest risk, 0% Apple fee, one billing stack. |
| **(b) External purchase link entitlement** | 3.1.1(a) StoreKit External Purchase Link Entitlement | **Not available on the India storefront** (EU/South Korea only; US needs none). | **Dead end for India. Discard.** |
| **(c) iOS-only IAP tier at higher price** | 3.1.1 straight compliance | **Medium.** StoreKit 2 integration in Flutter (`in_app_purchase` or RevenueCat), App Store Server Notifications → backend entitlement sync alongside Razorpay's, Apple's 15% Small Business Program fee (under $1M/yr) → price ₹999 tier at e.g. ₹1,199 IAP to preserve margin. Ongoing: two billing systems, two reconciliation paths, Apple's proration/refund rules. | **Viable phase-2** if web-portal conversion on iOS proves too lossy. Do not build for launch. |

---

## 4. Recommendation

**Ship: Razorpay in-app on Android via Google Play user-choice billing + web-portal purchase (Netflix pattern) on iOS.**

- **Android:** Enroll in UCB and present Razorpay alongside Play billing. This is the only compliant way to get Razorpay into the Android app in India today. Accept the ~11% Google service fee on Razorpay-routed transactions; monitor the June-2026 expanded-billing rollout, which would remove the concurrent-Play-billing requirement and cut fees if/when it reaches India. If the UCB integration overhead (PCI DSS certification + alt-billing APIs) is too heavy for launch, use the iOS web-portal flow on Android too and add UCB later — do **not** ship raw Razorpay checkout inside the Android app.
- **iOS:** Free companion app with **no purchase surface at all**; all purchasing on the Razorpay web portal, driven by QR on the box, onboarding emails, and WhatsApp. Skip the 3.1.3(e) gamble; India has no external-link entitlement and no live antitrust remedy. Keep IAP tier (option c) as a measured phase-2 if funnel data demands it.

**Confidence: HIGH (~85%)** on the iOS strategy — guideline text, entitlement availability, and every major hardware-companion precedent point the same way. **MEDIUM (~70%)** on the Android recommendation — the compliance picture is clear, but the UCB-integration-cost vs web-portal tradeoff, the pending Supreme Court appeal, and the possible India expansion of Google's June 2026 billing changes could all shift the optimal choice within 6–12 months.

---

## Sources

- [Apple App Review Guidelines (current)](https://developer.apple.com/app-store/review/guidelines/) — 3.1.1, 3.1.1(a), 3.1.3(a–g), 3.1.4
- [Google Play Payments policy](https://support.google.com/googleplay/android-developer/answer/9858738)
- [Google Play billing changes for India](https://support.google.com/googleplay/android-developer/answer/13306652)
- [Understanding user choice billing on Google Play](https://support.google.com/googleplay/android-developer/answer/13821247)
- [Android Developers Blog — Expanded billing choice and lower fees (June 2026)](https://android-developers.googleblog.com/2026/06/play-expanded-billing.html)
- [Miko App Store listing (IAP evidence)](https://apps.apple.com/us/app/miko-play-learn-connect/id1588895826) · [Miko Max help center](https://help.miko.ai/hc/en-us/articles/23964687347485-Miko-MAX-Subscription-Offers-and-Activation-Process)
- [Nest Aware purchase docs](https://support.google.com/product-documentation/answer/12657252) · [Ring Protect plans](https://ring.com/plans) · [Whoop membership management](https://support.whoop.com/s/article/Updating-Payment-Information)
- [MacRumors — Ninth Circuit modifies Epic injunction (Dec 2025)](https://www.macrumors.com/2025/12/11/apple-app-store-fees-external-payment-links/) · [9to5Mac — Apple SCOTUS petition (May 2026)](https://9to5mac.com/2026/05/21/apple-seeks-supreme-court-review-of-contempt-finding-and-injunction-scope-in-epic-games-case/)
- [MacRumors — India refuses to pause Apple CCI case (May 2026)](https://www.macrumors.com/2026/05/19/india-refuses-to-let-apple-pause-app-store-case/) · [Medianama — CCI/Apple non-cooperation (Jan 2026)](https://www.medianama.com/2026/01/223-indias-competition-regulator-apple-antitrust-case-non-cooperation/) · [Business Standard — Apple vs CCI](https://www.business-standard.com/technology/tech-news/apple-vs-cci-india-s-antitrust-case-reaches-key-stage-on-app-store-rules-126042700411_1.html)
- [Business Standard — NCLAT/Google Play billing case](https://www.business-standard.com/industry/news/google-cci-told-to-respond-to-firms-challenging-play-store-billing-policy-124051000923_1.html)
- [RevenueCat — app-to-web purchase guidelines](https://www.revenuecat.com/blog/engineering/app-to-web-purchase-guidelines) · [Apple external purchase link allowed-regions entitlement](https://developer.apple.com/documentation/BundleResources/Entitlements/com.apple.developer.storekit.custom-purchase-link.allowed-regions)
