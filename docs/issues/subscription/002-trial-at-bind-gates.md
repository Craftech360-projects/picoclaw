---
id: SUB-2
title: "Trial at bind → expired trial gates a session"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-1]
---

## Parent

Spec §4 trial lifecycle + §5 enforcement; tickets 003/005/009 in the wayfinder map.

## What to build

The first real gate, end to end. When a parent binds a device (6-digit code flow) and no `device_subscriptions` row exists for the MAC, create one: `status=trial`, `trial_ends_at=+30d`, plan=Family, `trial_used=true` — once per MAC ever (a re-bind of a MAC with an existing row creates nothing). The verdict computes trial expiry **lazily** (`status='trial' AND now() > trial_ends_at ⇒ lapsed`, repairing the row) — a cron job only sends the reminder pushes (day 23/27/30) and is never the enforcer. When the verdict refuses, the gateway skips LiveKit dispatch entirely and instead streams the pre-recorded English gate clip over the already-established UDP session (framed by `tts start/stop` MQTT signals), and manager-api fires the plan-gate FCM push to the parent.

Also produce the clip itself: one generated English recording ("ask Mumma or Papa to check the Cheeko app so we can keep playing"), stored where the gateway can stream it as 24kHz Opus frames.

## Acceptance criteria

- [ ] First-ever bind creates the trial row; second bind of the same MAC (any account) does not
- [ ] Verdict on a device 31 days past trial start returns `allowed:false, reason:no_plan` even if no cron ran, and the row now reads `lapsed`
- [ ] Refused session: no LiveKit room is created; `client.py` receives the clip audio over UDP with correct framing
- [ ] Plan-gate push reaches the bound parent's FCM token (trial-ended copy)
- [ ] Reminder job sends day-23/27/30 pushes exactly once each per device
- [ ] Devices with `status=trial` inside 30 days are allowed with Family-tier `remaining` values

## Blocked by

- SUB-1
