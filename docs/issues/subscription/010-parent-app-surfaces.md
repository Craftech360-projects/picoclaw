---
id: SUB-10
title: "Parent app: plan/usage view, gate screen, pushes"
type: AFK
status: closed
triage: afk-ready
assignee: claude
blocked-by: [SUB-3]
---

## Parent

Spec §6 app scope; wayfinder ticket 014 (direction A); app map in `docs/cheeko-system-overview.md` §6.

## What to build

Flutter surfaces, view-only (no purchase CTA — iOS-safe wording "Manage at pay.cheekoai.in"): a plan & usage screen (current plan pill, trial countdown, monthly question ring, daily question/image meters — driven by `GET /api/mobile/devices/:mac/subscription`), and a gate-moment screen reached from the plan-gate push (what's paused vs what still works, portal address). Wire FCM display for the new pushes — `flutter_local_notifications` is declared but currently unwired; add the foreground handler so trial/gate/80% pushes actually render when the app is open.

## Acceptance criteria

- [x] Plan & usage screen renders trial countdown and live bucket state for the selected device
- [x] Gate push received → tapping opens the gate screen with reason-appropriate copy
- [~] ~~No purchase button or checkout webview anywhere in the app; portal mentioned as text/QR only~~ — **obsolete** (see Resolution)
- [x] Foreground pushes display via flutter_local_notifications; background taps deep-link correctly *(terminated-state deep-link deferred)*
- [~] Sunshine direction styling consistent with the prototype *(uses the app's sunshine palette; not diffed against a prototype file)*
- [x] `flutter test` green

## Blocked by

- SUB-3

## Resolution

Shipped across two repos; ticket closed.

**Payment-model reconciliation:** SUB-10 was written under the retired
portal-only model. The 2026-07-21 rails amendment (`cheeko-subscription-spec.md`
§ header) moved purchases in-app to RevenueCat IAP, and SUB-16 already shipped a
working paywall. Per user decision, the gate screen's re-subscribe button routes
to that in-app paywall (`/subscription`) — App-Store-safe since IAP purchase CTAs
are allowed. Criterion 3 ("no purchase button / portal-only") is therefore
**obsolete**, not implemented as written.

**What shipped:**
- App (`feat/iap-subscription`, commit `cb9148e`):
  - `DeviceSubscription` parses plan limits + live usage buckets from
    `GET /toy/api/mobile/devices/:mac/subscription`.
  - Usage panel in `SubscriptionScreen` (trial banner already showed the
    countdown): monthly question ring + daily question/image meters, shown for
    active/trial/grace only. Minutes stay hidden (packaging spec: invisible).
  - New `GateScreen` — "Paused for now" (talk, pictures) vs "Still works" (cards,
    downloads) + reason-appropriate copy + re-subscribe → paywall.
  - FCM deep-link: foreground notifications now carry the FCM `data` as payload
    and route on tap; `onMessageOpenedApp` routes background taps to the gate
    screen. Foreground *display* was already wired in SUB-16.
- Backend (`Subscription_implemetation`, commit `463df66c`): `sendPushNotification`
  gained an optional `data` payload; the plan-gate push sends
  `{ type: 'plan_gate', reason, mac }` so the app can deep-link.

**Tests/review:** new unit + widget tests (model parse, gate copy, push routing,
usage panel) all green; `flutter analyze` clean on changed files; full
`flutter test` = 336 passed / 39 failed (39 = documented pre-existing
Firebase-init baseline, no new failures); backend `subscription.service.test.js`
64/64. Self-review (high effort) caught + fixed two issues: a cold-start
deep-link that got clobbered by the splash `pushReplacement` (removed; deferred),
and stale usage meters showing on a lapsed device's paywall (guarded on
`isActive`).

**Deferred / unverified:**
- Terminated-state deep-link (`getInitialMessage`) — the splash redirects with
  `pushReplacement`, so a gate push pushed at cold start is clobbered. Needs the
  splash to consume the pending message after it navigates. `onMessageOpenedApp`
  covers the app-in-background case.
- On-device push delivery + tap and the prototype visual diff were not exercised
  (no device/emulator in this session); logic is covered by tests.
