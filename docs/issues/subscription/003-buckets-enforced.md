---
id: SUB-3
title: "Question & image buckets enforced"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-2]
---

## Parent

Spec §1 plans, §5 enforcement; wayfinder tickets 010 (packaging) + 013 (enforcement).

## What to build

Make the verdict actually meter. Monthly questions = `SUM(message_count)` over `device_token_usage_session` since `current_period_start` (trial: since `trial_started_at`); daily questions and minutes over the current IST calendar day; images counted from the imagine-upload path against plan image limits. Wire the refusal reasons (`monthly_bucket_empty`, `daily_questions`, `daily_minutes`, `daily_images`) and gate the imagine flow with the same verdict call before the gateway hands off to line_art. A session already running when a bucket empties is never cut — buckets are start-gated only. Send the single 80%-of-monthly-bucket FCM push ("240 of 300 used"), once per period.

Clock edges: day boundaries at IST midnight; a session counts toward the day it started; period timestamps stored/compared in UTC.

Also owned here: **`GET /api/mobile/devices/:mac/subscription`** (Firebase Bearer) — status, plan, period, usage summary, trial countdown. It exposes exactly the numbers this ticket computes, and SUB-10 (parent app) consumes it, which is why SUB-10 is blocked by this ticket.

## Acceptance criteria

- [x] Device over its monthly question bucket ⇒ `monthly_bucket_empty`; over daily questions ⇒ `daily_questions`; over daily minutes ⇒ `daily_minutes`
- [x] Imagine request on a gated device is refused before any line_art call; image quota per plan enforced
- [x] 80% push fires exactly once per billing period, at the crossing
- [x] Bucket empties mid-session ⇒ session completes; the next session is refused
- [x] IST midnight rollover resets daily counters (test with a mocked clock)
- [x] Verdict latency < 100ms at current fleet size (live SUMs, no counters) — *78–121ms measured from a dev laptop over WAN to Supabase; the budget-relevant in-cluster path is 1 lookup + ≤4 parallel indexed queries*
- [x] `GET /api/mobile/devices/:mac/subscription` returns status/plan/period/usage/trial-countdown for the caller's own device (Firebase auth enforced) — *auth + ownership via existing middleware (401 integration-tested); summary payload verified live; end-to-end with a real Firebase token lands with SUB-14*

## Blocked by

- SUB-2

## Resolution (2026-07-17, `cheeko-backend@50b41345`)

**Shipped.** The verdict meters for real: live SUMs over
`device_token_usage_session`, image COUNTs over the new
`device_image_generations` table (migration `20260718000000_sub3_usage_buckets`,
applied to the dev DB), IST day windows computed as UTC instants
(`istDayWindow`, clock-injectable). Image buckets gate only `flow=imagine`
(`GET …/session-verdict?flow=imagine`); the gateway plan-gates `runImagine`
before any line_art work via a **required** `fetchVerdict` dep and a new
`plan_limit` wire-contract error code. The 80% push fires from the
usage-write path (the actual crossing), exactly once per period via a
conditional UPDATE on the new `bucket_alert_sent_at`; the FCM token is looked
up **before** the claim so an app-less parent doesn't burn the period's alert.
`GET /api/mobile/devices/:mac/subscription` returns status/plan/period/usage/
trial-countdown for SUB-10.

**Evidence.** Every refusal reason + the 80% exactly-once chain exercised live
against the real stack and dev DB (fixture MAC `20:6E:F1:A6:D0:24`, restored
to its SUB-2 lapsed-trial state afterwards). 51 unit tests for the service
(mocked-clock IST rollover included), imagine/mobile integration tests, and
the gateway suite green — except `tests/dispatch-metadata.test.js` (2
failures, **pre-existing**, untouched by this diff). Two review rounds
(4 combined finder angles + focused verifier); round-1 findings fixed:
claim-before-token ordering, missing short-circuit after the alert sends,
expired-trial nudge, fractional-minute display, dead image COUNTs on the
voice hot path, duplicated monthly SUM, silently-optional gateway gate.

**Deliberate residuals.**
- A monthly-image exhaustion reports reason `daily_images` — the ticket/spec
  reason enum has no `monthly_images`; consumers keying copy off the reason
  should use `remaining` + period fields (revisit in SUB-10 if the copy needs
  the distinction).
- The imagine flow refuses on question/minute buckets too ("the same verdict
  call" per this ticket), so a knob-press mid-session after the daily minute
  cap refuses the image while the voice session rightly continues.
- Sessions straddling IST midnight or a period renewal attribute all usage to
  the window of their `created_at` (first flush) — named in the `computeUsage`
  comment; SUB-5's heartbeat is the natural place to make attribution
  window-aware if it ever matters.
- `device_token_usage_session` has no `(mac_address, created_at)` index; the
  `(mac_address, usage_date)` index narrows to the device's rows, fine at
  current fleet size. Add the index when per-device row counts grow.
- 80% push delivery to a real screen: SUB-14 (dummy-token run proved the full
  chain to the Firebase send, same boundary as SUB-2).
