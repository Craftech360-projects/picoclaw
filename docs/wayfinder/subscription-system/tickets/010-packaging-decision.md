---
id: 10
title: Packaging decision — questions vs minutes
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: [8]
---

## Question

Which packaging ships customer-facing: **question buckets** (pricing doc §3: 100/300/800 per month + daily fair-use caps) or **talk-time minutes** (§4b: 60/200/500 min)?

- The pricing doc leans questions-outside/minutes-inside; the user deliberately deferred this pending real-parent input ([Parent pricing & packaging research](008-parent-packaging-research.md)).
- Whatever wins, the backend minute meter stays the enforcement backstop (§5 guardrail 1).
- Resolution must also fix: bucket reset semantics (billing-anniversary vs calendar month), the timezone for "day" caps (IST assumed), and whether unused questions roll over (recommend: no).

## Resolution (2026-07-14, grilling session — survey skipped)

Blocker [Parent research](008-parent-packaging-research.md) was closed out of scope (launch data replaces it); decided directly:

1. **Questions customer-facing, minutes enforced underneath** — pricing doc §3 buckets (100/300/800 per month) with daily fair-use caps; the invisible daily minute cap is the abuse backstop (§5 guardrail 1). 1 question = 1 user message (`message_count`, already metered).
2. **Bucket resets on the billing anniversary** (trial-start date during trial) — matches exactly what the parent paid for; no mid-month proration edge cases.
3. **No rollover** — use-it-or-lose-it; breakage funds the margin, buckets are sized generously.
4. **Day caps run on IST calendar days** (India-only launch).
5. Validation moved post-launch: watch trial→paid conversion and bucket-consumption distribution from the field-fleet trial cohort; the [survey instrument](../research/parent-pricing-survey.md) remains available if numbers disappoint.
