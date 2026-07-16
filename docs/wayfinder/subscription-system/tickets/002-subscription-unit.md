---
id: 2
title: Subscription unit
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

What does a plan attach to — device, parent account, or kid profile?

## Resolution (2026-07-14, charting session)

**Per device; the parent account is the payer** (and may own several device subscriptions).

- Matches trial-per-activation, per-device metering (`device_token_usage_session` keyed by MAC), and Phase 2 enforcement exactly.
- Second toy = second subscription; no shared-bucket accounting, no one-sub-many-toys abuse.
- Premium's "2 kid profiles" perk remains an account-level feature flag unlocked by the device's plan tier — spec must state this precisely (falls to the schema ticket).
