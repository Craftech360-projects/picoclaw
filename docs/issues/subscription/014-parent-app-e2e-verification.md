---
id: SUB-14
title: "Parent-app e2e verification — pushes observed on a real phone"
type: HITL
status: open
triage: needs-human
blocked-by: []
---

## Parent

Created at SUB-2 close (2026-07-17). Collects every "needs a real phone" observation the epic deferred, so agent tickets never block on the user's hands.

## What to verify

Everything below is already coded and verified up to the Firebase send; the only unobserved step is a push rendering on a physical phone. Requires: parent app logged in against the backend sharing the dev Supabase DB (so FCM registration replaces the dummy token — `push_notification_registration_service.dart` handles it automatically on login).

Runbook for the trial-ended push is in the 2026-07-17 handoff and SUB-2 part 3: real token in `parent_profile.fcm_token` (must not be the dummy string), expired-trial `device_subscriptions` row (MAC UPPERCASE with colons), fire `GET /toy/device/{mac}/session-verdict` with `X-Service-Key`, `ENFORCEMENT_ENABLED=true`.

## Acceptance criteria

- [ ] FCM registration on login replaces `dummy-fcm-token-sub2-smoke-test` with a real token (from SUB-2)
- [ ] "Cheeko's free trial has ended" push appears on the phone (SUB-2 criterion 4 delivery leg)
- [ ] Day-23/27/30 trial reminder push appears on the phone (SUB-2 criterion 5 delivery leg)
- [ ] 80%-of-monthly-bucket push appears on the phone (SUB-3 delivery leg — verify after SUB-3 ships)

## Blocked by

Nothing hard — runnable whenever the user has the phone. The 80% criterion needs SUB-3 shipped first.
