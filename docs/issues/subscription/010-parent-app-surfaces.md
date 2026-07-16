---
id: SUB-10
title: "Parent app: plan/usage view, gate screen, pushes"
type: AFK
status: open
triage: afk-ready
blocked-by: [SUB-3]
---

## Parent

Spec §6 app scope; wayfinder ticket 014 (direction A); app map in `docs/cheeko-system-overview.md` §6.

## What to build

Flutter surfaces, view-only (no purchase CTA — iOS-safe wording "Manage at pay.cheekoai.in"): a plan & usage screen (current plan pill, trial countdown, monthly question ring, daily question/image meters — driven by `GET /api/mobile/devices/:mac/subscription`), and a gate-moment screen reached from the plan-gate push (what's paused vs what still works, portal address). Wire FCM display for the new pushes — `flutter_local_notifications` is declared but currently unwired; add the foreground handler so trial/gate/80% pushes actually render when the app is open.

## Acceptance criteria

- [ ] Plan & usage screen renders trial countdown and live bucket state for the selected device
- [ ] Gate push received → tapping opens the gate screen with reason-appropriate copy
- [ ] No purchase button or checkout webview anywhere in the app; portal mentioned as text/QR only
- [ ] Foreground pushes display via flutter_local_notifications; background taps deep-link correctly
- [ ] Sunshine direction styling consistent with the prototype
- [ ] `flutter test` green

## Blocked by

- SUB-3
