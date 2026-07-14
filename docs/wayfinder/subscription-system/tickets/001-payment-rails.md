---
id: 1
title: Payment rails
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

Which payment rails should the subscription use — Razorpay-style external checkout, native app-store IAP, or hybrid?

## Resolution (2026-07-14, charting session)

**Razorpay Subscriptions with UPI Autopay + cards, purchase flow in the parent app via webview/browser.**

- ~2% fees vs 15–30% app-store cut, which the ₹199–999 margin math cannot absorb.
- UPI Autopay is how Indian parents actually pay; unavailable via Apple IAP.
- Justification for external billing: the service is consumed on the physical toy, not in the app (Uber/Amazon pattern).
- Known risk: Apple App Store review may push back → mitigation strategy is the subject of [App-store policy research](007-app-store-policy-research.md); worst case iOS shows no purchase button (Netflix pattern).
