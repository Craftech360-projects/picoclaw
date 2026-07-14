---
id: 11
title: Field-device migration at launch
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

What happens to toys already activated in the field on the day subscriptions go live?

- They predate the trial system — do they get a fresh 1-month trial from launch day ("retro-trial"), a longer grandfathering window, or immediate gating?
- How is this communicated to existing parents (in-app + push before the switch flips)?
- Is there a kill-switch/rollout flag so enforcement can be enabled gradually (e.g. per-device cohort) rather than platform-wide at once?
- Recommend: retro-trial from launch day + staged rollout flag — but decide with real device counts (query `ai_device` for active fleet size first).

## Resolution (2026-07-14, grilling session)

- **Fleet size: under 100 devices** (per user) — migration is a switch-flip, not a program.
- **Existing fleet gets a fresh 1-month trial from launch day**: standard trial clock (Family-tier limits, day-23/27/30 reminders per [Trial lifecycle design](005-trial-lifecycle-design.md)), then normal gating. One code path — the migration is just seeding trial records `trial_started_at = launch_day` for all bound MACs.
- **Rollout control: global kill-switch only** — one flag turning enforcement on/off platform-wide for instant revert; no per-device cohort machinery (the trial month itself is the staging runway; fleet too small to need more).
- **Comms**: in-app announcement + one push before launch day explaining plans and the free month; at <100 devices, personally reachable owners make this low-risk. Copy details fall to the spec's notification section.
