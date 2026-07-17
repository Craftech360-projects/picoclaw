---
id: SUB-5
title: "Usage heartbeat + mid-session minute cutoff"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-3, SUB-4]
---

## Parent

Spec §3 (`usage-heartbeat`) + §5; `plan-usage-tracking-and-limits.md` Phase 1.4; wayfinder ticket 013 (v1 includes hard cutoff).

## What to build

The worker POSTs `usage-heartbeat` for each live session every 5 minutes (tokens/duration so far). Manager-api updates the in-flight session row and recomputes today's minutes; when the daily minute cap is breached mid-session, the heartbeat response carries `{cutoff:true}` and the session is torn down through the existing `end_prompt` path — the child hears a graceful "time for a break", not a drop. Side benefit: a crashed worker loses at most 5 minutes of billable usage.

Cutoff applies ONLY to the daily minute cap (the abuse backstop). Question buckets never cut mid-session.

## Acceptance criteria

- [x] Heartbeats arrive every ~5 min per live session; each updates the session row — *endpoint live-verified (row created + cumulatively overwritten per beat); worker loop verified by Go tests against a real HTTP server; full in-room run blocked by a local LiveKit media wedge (see Resolution)*
- [x] Daily minute cap breached mid-session ⇒ next heartbeat returns `cutoff:true` ⇒ session ends via `end_prompt` within seconds — *cutoff response live-verified against dev DB; cutoff→graceful-farewell path unit-tested (same `handleEndPrompt` the gateway's `end_prompt` uses)*
- [x] Question bucket reaching zero mid-session does NOT trigger cutoff — *live-verified: 50 msgs (> daily 40) with 5 min used ⇒ `cutoff:false`; the check reads only `session_duration_seconds`*
- [x] Kill worker mid-session ⇒ usage row exists with duration accurate to ≤5 min — *each beat persists the cumulative row (verified live); a killed worker leaves the last beat's row*
- [x] Cutoff event logged and visible in the admin metrics (gate-hit counts) — *`[SUBSCRIPTION] Heartbeat cutoff for <mac>: daily_minutes (16.0/15 min)` observed live; same log-line metric surface as SUB-3's verdict refusals (no gate-hit table exists yet — SUB-11)*

## Blocked by

- SUB-3, SUB-4

## Resolution (2026-07-17, picoclaw@b558664 + cheeko-backend@5b3dbc21)

**Shipped.** Worker: `pkg/livekit/usage_heartbeat.go` — per-session goroutine
(worker.go ticker style, `USAGE_HEARTBEAT_INTERVAL` env, default 5m) POSTs the
cumulative `UsageSnapshot` to the new endpoint with service-key headers; on
`{cutoff:true}` it fires the existing `handleEndPrompt` farewell ("time for a
little break") and stops; failed beats warn and never cut. Started from
`Join` after track publish; skipped in file-memory-only mode. Manager:
`POST /device/:mac/usage-heartbeat` (requireServiceKey, sessionId required)
reuses `recordTokenUsage` — cumulative overwrite by `session_id`, delta into
the daily aggregate, so replays are idempotent and the 80% bucket alert now
fires mid-session — then `subscriptionService.heartbeatCutoff`: enforcement
on + plan present + IST-day `session_duration_seconds` SUM ≥
`daily_minutes_limit` ⇒ cutoff. Everything else fails open.

**Evidence.** 9 new service unit tests + 2 auth integration tests (66 total
green in touched suites; 11 full-suite failures are pre-existing, none in
touched files); 5 Go tests (loop cutoff-once, error-survival, envelope parse,
header/path/payload shape) — only pre-existing
`TestSynthesizeAndPlayLogsTTSProviderType` fails in pkg/livekit, as
documented in SUB-4. Live against the real stack + dev DB (fixture MAC
`20:6E:F1:A6:D0:24`, family plan, 15 min/day): auth 401s, 400 without
sessionId, row create → cumulative overwrite (no double-count in the daily
aggregate), questions-exhausted ⇒ no cut, 16/15 min ⇒
`{cutoff:true, reason:daily_minutes}` + gate-hit log. All test rows deleted
afterwards.

**Full e2e verified** (same day, after a Docker restart un-wedged the local
LiveKit/EMQX containers): `client.py --mode voice` as the real device →
gateway hello → LiveKit room `…_206EF1A6D024_conversation` → worker
`Usage heartbeat started (interval=20s)` → beats at exactly 20s
(`cutoff=false`) → seeded 15 min of prior usage → next beat:
`Heartbeat cutoff … daily_minutes (16.3/15 min)` + `cutoff=true` → worker
"Daily minute cap breached — ending session gracefully" → farewell TTS burst
received by the client → final usage summary persisted. Nine seconds from
breach to full teardown. Test rows deleted, fixture restored to lapsed.
Gateway note for future harnesses: `lk dispatch --metadata` alone is not
enough — `RoomSession.deviceMAC` comes from room name/metadata, which the
gateway provides in production.

**Residuals.**
- A final-summary POST racing an in-flight heartbeat can double-count the
  overlap delta in `device_token_usage` (read-then-update, no transaction) —
  pre-existing pattern in `recordTokenUsage`, window is milliseconds.
- SUB-3's midnight/renewal attribution note stands: heartbeat-updated rows
  still attribute to the `created_at` window.
- SUB-4's unbilled in-flight `maybeSummarize` race is unchanged; its tokens
  now at worst miss one beat and land in the next.
