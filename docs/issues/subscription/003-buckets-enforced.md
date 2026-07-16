---
id: SUB-3
title: "Question & image buckets enforced"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-2]
---

## Parent

Spec §1 plans, §5 enforcement; wayfinder tickets 010 (packaging) + 013 (enforcement).

## What to build

Make the verdict actually meter. Monthly questions = `SUM(message_count)` over `device_token_usage_session` since `current_period_start` (trial: since `trial_started_at`); daily questions and minutes over the current IST calendar day; images counted from the imagine-upload path against plan image limits. Wire the refusal reasons (`monthly_bucket_empty`, `daily_questions`, `daily_minutes`, `daily_images`) and gate the imagine flow with the same verdict call before the gateway hands off to line_art. A session already running when a bucket empties is never cut — buckets are start-gated only. Send the single 80%-of-monthly-bucket FCM push ("240 of 300 used"), once per period.

Clock edges: day boundaries at IST midnight; a session counts toward the day it started; period timestamps stored/compared in UTC.

Also owned here: **`GET /api/mobile/devices/:mac/subscription`** (Firebase Bearer) — status, plan, period, usage summary, trial countdown. It exposes exactly the numbers this ticket computes, and SUB-10 (parent app) consumes it, which is why SUB-10 is blocked by this ticket.

## Acceptance criteria

- [ ] Device over its monthly question bucket ⇒ `monthly_bucket_empty`; over daily questions ⇒ `daily_questions`; over daily minutes ⇒ `daily_minutes`
- [ ] Imagine request on a gated device is refused before any line_art call; image quota per plan enforced
- [ ] 80% push fires exactly once per billing period, at the crossing
- [ ] Bucket empties mid-session ⇒ session completes; the next session is refused
- [ ] IST midnight rollover resets daily counters (test with a mocked clock)
- [ ] Verdict latency < 100ms at current fleet size (live SUMs, no counters)
- [ ] `GET /api/mobile/devices/:mac/subscription` returns status/plan/period/usage/trial-countdown for the caller's own device (Firebase auth enforced)

## Blocked by

- SUB-2
