---
id: SUB-5
title: "Usage heartbeat + mid-session minute cutoff"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-3, SUB-4]
---

## Parent

Spec §3 (`usage-heartbeat`) + §5; `plan-usage-tracking-and-limits.md` Phase 1.4; wayfinder ticket 013 (v1 includes hard cutoff).

## What to build

The worker POSTs `usage-heartbeat` for each live session every 5 minutes (tokens/duration so far). Manager-api updates the in-flight session row and recomputes today's minutes; when the daily minute cap is breached mid-session, the heartbeat response carries `{cutoff:true}` and the session is torn down through the existing `end_prompt` path — the child hears a graceful "time for a break", not a drop. Side benefit: a crashed worker loses at most 5 minutes of billable usage.

Cutoff applies ONLY to the daily minute cap (the abuse backstop). Question buckets never cut mid-session.

## Acceptance criteria

- [ ] Heartbeats arrive every ~5 min per live session; each updates the session row
- [ ] Daily minute cap breached mid-session ⇒ next heartbeat returns `cutoff:true` ⇒ session ends via `end_prompt` within seconds
- [ ] Question bucket reaching zero mid-session does NOT trigger cutoff
- [ ] Kill worker mid-session ⇒ usage row exists with duration accurate to ≤5 min
- [ ] Cutoff event logged and visible in the admin metrics (gate-hit counts)

## Blocked by

- SUB-3, SUB-4
