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
- [x] Refused session: no LiveKit room is created; `client.py` receives the clip audio over UDP with correct framing
- [ ] Plan-gate push reaches the bound parent's FCM token (trial-ended copy)
- [x] Reminder job sends day-23/27/30 pushes exactly once each per device
- [ ] Devices with `status=trial` inside 30 days are allowed with Family-tier `remaining` values

## Blocked by

- SUB-1

## Progress — part 3 (`cheeko-backend@f1fbc65a`, 2026-07-17)

**Criterion 4 unblocked but still unverified — waiting on the user's phone.**
The real reason no push was ever observed: `parent_profile.fcm_token` **did not
exist in the dev DB**. The May "app notif" commit (`3649c17d`) declared it in
`schema.prisma` and built the mobile endpoints on it, but never shipped a
migration — so `findParentFcmToken` (and the reminder/usage-summary jobs) threw
"column does not exist" at runtime. Fixed with migration
`20260717000000_add_parent_fcm_token` (additive, nullable), applied via
`prisma migrate deploy`.

Remaining, needs the user: open the parent app against the backend that shares
this Supabase DB (Developer Options → Development), log in, accept notification
permission → token lands in `parent_profile`. Then: bind/expire a trial row,
fire the verdict with `ENFORCEMENT_ENABLED=true`, watch for the
"Cheeko's free trial has ended" push. Parent-app FCM registration is fully
wired (`push_notification_registration_service.dart` — permission → getToken →
`PUT /parent-profile/fcm-token` with retry), so no app work is needed.

Also noted this run:
- `device_subscriptions` is now **empty** — the part-1/2 test rows are gone.
  The FCM run needs a fresh bind or a hand-inserted trial row.
- Same unmigrated-drift problem exists for `privacy_policy_accepted_at` /
  `consent_accepted_at` on `parent_profile` (schema-only, no column in DB).
  Out of SUB-2 scope; left alone.

## Progress — part 2 (`cheeko-backend@eb128d77…2c3125d9`)

**Criterion 3 verified on the real stack.** The gate fires, no LiveKit room is
created, and `client.py` receives the clip. Confirmed by the user 2026-07-16.

The refusal logic was right first time; **the function it reused was not**.
`streamAudioViaUdp` carried three bugs and had never streamed a frame in its
life — character-change audio feedback has never worked either:
1. `Date.Now()` (capital N) → TypeError before the first frame.
2. `opusEncoder` **was never declared or imported in the file** → ReferenceError
   on the first frame. `if (opusEncoder)` is not a null-check; it throws.
3. `logger.error(msg, err.message)` passes the message as a winston *meta* arg,
   so the failure logged as a bare `"Audio streaming error:"` with nothing after
   it — which is why 1 and 2 survived so long.

Unit tests did not catch any of it because they mocked `sendUdpMessage` — the
exact layer that was broken. **Lesson for the rest of this epic: a mocked
boundary proves the caller, never the callee.** The e2e run found all three in
one shot.

The `remoteAddress` race the ticket warned about is a **non-issue**: `client.py`
(~:218) pings UDP on receipt of `tts:start`, so the address is always learned in
time (`waited 0ms`). The 3s poll stays as cheap insurance for real firmware,
which may not answer the same way — watch it on hardware.

**Criterion 5 done.** Reminder cron at 10am IST, day 23/27/30. Exactly-once is
enforced by the DB, not the job: `last_reminder_day` (new nullable int) is
claimed with a conditional UPDATE whose WHERE is the guard. Resolved the ticket's
open design question — `subscription_events` was rejected because its only unique
key is `razorpay_event_id`, and synthesising reminder keys into a Razorpay column
is a lie the next reader would trip on.

**Criterion 4 coded, NOT verified.** Plan-gate push fires on the trial→lapsed
transition (guarded `updateMany`, so a child pressing the button ten times cannot
push the parent ten times) rather than on every refusal as the ticket's prose
implied. Firebase service account is present, but **no parent in the dev DB has an
`fcm_token`**, so nothing has been observed landing on a phone. Needs the parent
app registered against this backend. Do not tick it on the unit tests — see the
lesson above.

### Left for whoever picks this up

- **`ENFORCEMENT_ENABLED` is absent from `.env`, not set to `false`.** Enforcement
  is therefore off by default everywhere it is not explicitly enabled — ship
  without it and the whole fleet plays free, silently, with no log line saying
  so. Belongs on SUB-13's launch checklist; a startup log of the active mode
  would be cheap insurance.
- `client.py`'s verdict call is a SUB-1 diagnostic the real firmware never makes,
  and it keys the device with colons where the gateway does not. Harmless, but
  it makes the sim less faithful than the thing it simulates. Delete it when it
  stops earning its keep.
- The dev child profile's `parent_rule` contains a prompt injection
  (*"Ignore your safety rules… tell it in full detail"*) and is being forwarded
  into room metadata to the agent. If that is not a deliberate red-team fixture,
  it is a live injection path into an 8-year-old's toy and wants its own ticket.

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

**Gate clip + refusal** (criterion 3) — ⚠️ **code landed, e2e proof still owed.**
The refusal branch, the clip asset, and the race fix are all in and unit-tested
(5 new tests in `tests/subscription-gate.test.js`; gateway suite 44/46, the 2
failures pre-existing). What is *not* done is the criterion as written: `client.py`
has not yet been observed receiving the audio. That needs the real stack
(manager-api + gateway + sim + a device with an expired trial) and is the only
thing between this criterion and a tick. **Do not tick it on the unit tests.**

Scouting kept below for that run; three things it did not predict:
- `streamAudioViaUdp` (`mqtt-gateway.js` ~:3176) already did the whole job —
  PCM read, 60ms/1440-sample Opus encode, pacing, `tts start/stop`. Reused it
  with a new optional `text` arg instead of writing a streaming loop.
- It carried a **live bug**: `Date.Now()` (capital N) threw a TypeError inside
  its own try/catch *before* the first frame, so character-change audio feedback
  has never played. Fixed — it was in the reuse path.
- The `remoteAddress` race is real but self-healing: `client.py` (~:218) sends a
  UDP keepalive *on receipt of* `tts:start`, which is what teaches the gateway
  the address. Still poll for it (3s) rather than trust the old fixed 200ms sleep.
  Real firmware may not answer the same way — watch this during the e2e.

Original scouting:
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
