---
id: SUB-2
title: "Trial at bind → expired trial gates a session"
type: AFK
status: open
triage: afk-ready
assignee: claude
blocked-by: [SUB-1]
---

## Parent

Spec §4 trial lifecycle + §5 enforcement; tickets 003/005/009 in the wayfinder map.

## What to build

The first real gate, end to end. When a parent binds a device (6-digit code flow) and no `device_subscriptions` row exists for the MAC, create one: `status=trial`, `trial_ends_at=+30d`, plan=Family, `trial_used=true` — once per MAC ever (a re-bind of a MAC with an existing row creates nothing). The verdict computes trial expiry **lazily** (`status='trial' AND now() > trial_ends_at ⇒ lapsed`, repairing the row) — a cron job only sends the reminder pushes (day 23/27/30) and is never the enforcer. When the verdict refuses, the gateway skips LiveKit dispatch entirely and instead streams the pre-recorded English gate clip over the already-established UDP session (framed by `tts start/stop` MQTT signals), and manager-api fires the plan-gate FCM push to the parent.

Also produce the clip itself: one generated English recording ("ask Mumma or Papa to check the Cheeko app so we can keep playing"), stored where the gateway can stream it as 24kHz Opus frames.

## Acceptance criteria

- [x] First-ever bind creates the trial row; second bind of the same MAC (any account) does not
- [x] Verdict on a device 31 days past trial start returns `allowed:false, reason:no_plan` even if no cron ran, and the row now reads `lapsed`
- [ ] Refused session: no LiveKit room is created; `client.py` receives the clip audio over UDP with correct framing
- [ ] Plan-gate push reaches the bound parent's FCM token (trial-ended copy)
- [ ] Reminder job sends day-23/27/30 pushes exactly once each per device
- [ ] Devices with `status=trial` inside 30 days are allowed with Family-tier `remaining` values

## Blocked by

- SUB-1

## Progress — part 1 done (`cheeko-backend@d18e29e6`)

**Landed and verified** (criteria 1–2): `ensureTrialForMac()` grants a 30-day
Family trial on first-ever bind, wired into both `bindDevice` paths and made
non-fatal so a subscription hiccup never fails a parent's pairing. The verdict
expires trials lazily and repairs the row to `lapsed`. Dev-DB evidence: 1st bind
→ `trial/trial_used=true/30d/plan_id=2`; re-bind from a *different* account →
same row, original payer intact; trial expired by 1 day → `no_plan` + row
repaired, no cron. 319 unit tests green (23 for this service).

**Criterion 6 is deferred to SUB-3 by decision** (confirmed with the user): SUB-3
owns the usage SUMs, and SUB-2 has no metering, so reporting full plan limits as
`remaining` would claim 300 left for a device that used 250. `remaining` stays
all-null (unknown) until SUB-3 makes it true.

### Remaining work, with the groundwork already scouted

**Gate clip + refusal** (criterion 3) — the substantial piece:
- Frames go out via `virtual-connection.js` `sendUdpMessage(payload, timestamp)`
  (~:347), which already owns sequence numbering, header, and encryption. Feed
  it Opus frames; do not re-implement that.
- ⚠️ **Race worth designing around**: `sendUdpMessage` silently drops when
  `this.udp.remoteAddress` is unset (~:349), and it is only learned from the
  device's *first UDP packet*. `_deferredSetup` can reach the refusal branch
  before that ping lands, so a naive implementation loses the clip's opening
  frames. Wait for `remoteAddress` (or buffer) before streaming.
- Audio contract from the hello response (~:477): **24 kHz, mono, 60 ms frames,
  opus**. `@discordjs/opus` is already a gateway dependency — no new dep needed.
- Framing signals: `{type:"tts", state:"start"|"stop", text, session_id}`
  published to `devices/p2p/${clientId}` (pattern at `mqtt-gateway.js` ~:1819).
- Refusal branch goes where SUB-1 left the seam: the verdict already rides the
  `Promise.allSettled` batch in `_deferredSetup` and is deliberately not
  destructured. Read it there and return before the "Step 2: LiveKit room setup"
  block (~:641) so no room is created.
- Clip asset: generated once at build time, English only (wayfinder 009). Needs a
  TTS provider + key; check `tts_providers` / `.env`.

**Plan-gate FCM push** (criterion 4): `src/services/pushNotification.service.js`
and `src/jobs/usageSummaryNotification.js` are the existing patterns. Likely
**not verifiable locally** without real FCM credentials — note it, don't fake it.

**Reminder job** (criterion 5): day 23/27/30, exactly once each. Cron pattern at
`src/jobs/` (`dailyEmailReport.js`, `usageSummaryNotification.js`). Needs an
idempotency record so a re-run or restart cannot double-send — the schema has no
column for that yet, so this needs a decision (new column vs. derive from
`subscription_events`).

### Note for SUB-1's record

The spec's claim that "unbind DELETES the `ai_device` row (`device.service.js:184`)"
is only half true: `unbindDevice` hard-deletes **only** when called with
`options.hardDelete`; the default path soft-clears `user_id`/`agent_id` (:190).
MAC-keying `device_subscriptions` is still correct — the hard-delete path exists
and a soft unbind still detaches the payer — but the reasoning should cite the
real behavior.
