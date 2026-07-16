---
id: 5
title: Trial lifecycle design
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

Pin down the trial's full lifecycle:

1. **What event is "first activation"?** Candidates in the current flow: `POST /toy/ota/activate` (device-side OTA activation), parent-side activation-code validation, device↔agent bind (`POST /agents/{id}/bind/{deviceCode}`), or first voice session. Each has different abuse/UX properties (e.g. a toy activated in the factory QA line must not burn its trial). Explore the codebase: manager-api `ota` + activation services, `ai_device` fields.
2. **Trial → paid conversion UX**: when and where the parent is asked to pick a plan (during trial? at expiry? in-app paywall placement).
3. **Reminder timeline**: which notifications fire at which trial days (e.g. day 23 "trial ends in a week", day 30 "trial ended").
4. Does the trial device row need Razorpay linkage at all, or is trial purely local state until first purchase? (Recommend: purely local — no card upfront.)

## Resolution (2026-07-14, grilling session)

1. **Trigger: first-ever successful bind.** The trial clock starts when a parent first binds the MAC to their account (`deviceBind`, manager-api `device.service.js:81` — the moment a real family owns the toy). Factory/QA only touches the OTA step and never burns a trial. **Codebase finding: unbind DELETES the `ai_device` row (`device.service.js:184`)** — so the permanent trial record lives in a separate MAC-keyed table that survives unbind/rebind (feeds [Schema design](012-subscription-schema-design.md)).
2. **Trial end: gate at expiry** with the kid-friendly notice. The 3-day grace is for failed renewals of paying customers only — trial deadlines are hard, reminders make them fair.
3. **Reminders**: persistent "X days left" banner in the app's plan/usage view from day 1; FCM pushes at day 23 ("1 week left"), day 27 ("3 days — pick a plan"), day 30 ("trial ended"). Three pushes max.
4. **No Razorpay linkage during trial** — no card upfront; trial is purely our state (Razorpay research confirmed native trials would force a ₹5 mandate auth). Razorpay enters at first purchase only.
