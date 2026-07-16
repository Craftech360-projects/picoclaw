---
id: 8
title: Parent pricing & packaging research
type: wayfinder:task
status: closed
assignee: rahul
blocked-by: []
---

## Question

HITL task — real-parent input the packaging decision waits on (this is the `docs/cheeko-pricing-strategy.md` §7 open validation):

1. **Van Westendorp price-sensitivity check** on ₹199 / ₹499 / ₹999 with Indian parents (target ~15–20 respondents). Method reference: `pricing` skill, `references/research-methods.md`.
2. **Framing preference**: "100 questions/month" vs "60 minutes of talk time/month" — which do parents understand and trust more? A/B the two §3 vs §4b tables from the pricing doc.
3. Capture willingness-to-pay for the trial→paid conversion moment specifically ("your toy's free month is ending — which plan?").

The agent prepares the survey/interview script and analysis; the human runs it with parents. Resolution records the findings and links raw results.

## Progress (2026-07-14)

**Agent's part done — instrument ready: [research/parent-pricing-survey.md](../research/parent-pricing-survey.md).** 10-minute call script: framing A/B (randomized card order + comprehension checks), Van Westendorp on the hero plan, Gabor-Granger at ₹499/₹299/₹699, annual appetite, and the trial-conversion-moment question — plus the analysis decision rules that feed [Packaging decision](010-packaging-decision.md) and a per-respondent logging sheet. n≈20 caveat noted: directional, not statistical.

**Ticket stays OPEN — awaiting the human part:** run it with 15–20 parents, fill the logging sheet, then resolve this ticket with the findings.

## Closed OUT OF SCOPE (2026-07-14)

User redrew the boundary: pre-launch survey **skipped**. At <100 devices, launch itself is the better price test — every field device gets a 1-month trial, so trial→paid conversion and real usage distribution replace the interviews; prices are DB-driven and cheap to change. [Packaging decision](010-packaging-decision.md) was resolved directly on the pricing doc's recommendation instead. The [survey instrument](../research/parent-pricing-survey.md) stays available if post-launch validation wants it.
