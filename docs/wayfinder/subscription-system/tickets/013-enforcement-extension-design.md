---
id: 13
title: Enforcement extension design
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: [10]
---

## Question

Extend the Phase 2 enforcement design (`docs/plan-usage-tracking-and-limits.md` §2 — daily-cap check in gateway `_deferredSetup` before LiveKit dispatch) to full subscription awareness:

1. **Session-start check**: one manager-api call returning the device's verdict `{allowed, reason, remaining}` from: subscription status (per [Lapse behavior](004-lapse-behavior.md)) + monthly bucket + daily cap. On refusal, gateway plays the gate notice per [Kid-facing lapse experience](009-kid-facing-lapse-experience.md) (pre-recorded clip over the existing UDP session). Where does this endpoint live; cache/TTL; fail-open or fail-closed when manager-api is down (recommend fail-open + alert — never brick on our outage).
2. **Question counting**: increment on user message (already in `message_count`) — define exactly when a "question" is consumed (mid-session bucket exhaustion behavior: finish session? warn? cut at next silence?).
3. **Imagine gating**: the imagine flow bypasses LiveKit (gateway → line_art WS) — gate it at the same gateway check; image quota per plan.
4. **Mid-session cutoff**: is Phase 1.4 heartbeat required for v1, or is start-of-session gating + max-session-60min enough? (Recommend: start-gating only for v1; a session can overshoot by one session-length, bounded by the 60-min cap.)
5. Music/story (Cerebrium bots) — metered/gated or free? They cost differently.
6. **Usage-alert UX** (graduated from fog once packaging was decided): when/how the parent app warns about bucket consumption — e.g. push at 80% of monthly questions, passive display for daily caps (per [Kid-facing lapse experience](009-kid-facing-lapse-experience.md): no daily-cap pushes).

## Resolution (2026-07-14, grilling session)

1. **Verdict endpoint**: `GET /device/:mac/session-verdict` on manager-api (service-key auth) returning `{allowed, reason ('ok'|'no_plan'|'monthly_bucket_empty'|'daily_questions'|'daily_minutes'), remaining:{questions_month, questions_today, minutes_today}}`. Gateway calls it in `_deferredSetup` before LiveKit room creation; refusal ⇒ skip dispatch + play the gate clip ([Kid-facing lapse experience](009-kid-facing-lapse-experience.md)). No caching (fresh verdict per session; <100 devices). **Fail-open + alert**: manager-api unreachable ⇒ allow the session, fire the fail-open alert ([Admin & ops](015-admin-ops-surface.md)) — never brick toys on our outage.
2. **Question counting**: 1 user message = 1 question, from `message_count` (already metered). **Bucket checked at session start only — a session in flight always finishes gracefully**; worst case one session's overshoot, bounded by the 60-min max and the minute cutoff below. Next session gets the gate.
3. **Imagine gating**: same verdict call before the line_art WS handoff; image quotas (per plan row) added to the verdict payload.
4. **Mid-session cutoff ships in v1** (user chose the stronger build): Phase 1.4 usage heartbeat (worker → manager-api every 5 min) **is now a launch prerequisite**, enabling a hard "time's up" (gateway `end_prompt`) when the **daily minute cap** is breached mid-session. Coherent split: minute cap = hard mid-session cutoff (abuse backstop); question bucket = start-gate + graceful finish (UX kindness).
5. **Music/story modes: no policy needed — Cheeko no longer uses the Cerebrium music/story bots** (user, 2026-07-14). Note: `docs/cheeko-system-overview.md` still describes them; flag for a doc sweep during implementation.
6. **Usage alerts**: one push at 80% of the monthly question bucket ("240 of 300 used"); daily caps stay push-free; everything else passive in the app's plan/usage view.
