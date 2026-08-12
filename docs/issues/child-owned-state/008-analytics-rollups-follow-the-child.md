---
status: open
assignee:
---

# 008 — Analytics rollups follow the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

The parent progress screens read daily rollups keyed on `mac_address`, so a child
changing toys sees their charts reset to zero and a sibling inherits their totals.

Broad but shallow: each table takes one nullable `kid_id`, one index, one backfill,
and one changed grouping in the read path. No logic changes.

**Migrate exactly these seven**, which the survey confirmed live:

| Table | Rows on DB1 |
|---|---|
| `device_analytics_event` | 5,625 |
| `rfid_card_tap_log` | 616 |
| `device_games_played` | 492 |
| `device_radio_played` | 122 |
| `device_usage_daily` | 95 |
| `device_ai_interactions_daily` | 79 |
| `device_card_taps_daily` | 40 |

**Do not migrate** `analytics_streaks`, `analytics_user_progress`,
`analytics_game_attempts`, `analytics_game_sessions`, `analytics_media_playback`,
`game_session` or `kid_activity_log`. All seven have zero rows and zero lifetime
inserts on DB1 — nothing in the gateway, the worker or the manager writes them. They
are removed in 009. If one of them turns out to have a producer that simply has not
run on the dev box, move it into this list rather than leaving it half-done.

`rfid_card_tap_log` already has a `kid_id` column; check whether it is populated
before adding another migration for it.

The daily-rollup writers already load the device row, so the child is in hand at
write time. Reads keep the same MAC fallback shape as 002.

One thing to leave alone: the parent push notification aggregates per device and
names the toy, not the child. Attributing it per child is a product decision about
notification copy, not part of this slice.

## Acceptance criteria

- [ ] The seven live tables have `kid_id`, indexed, backfilled from `ai_device`
- [ ] Rollup writers populate it at write time from the device row already loaded
- [ ] The parent progress summary, details and trend endpoints resolve by child, with
      the null-guarded MAC fallback for unpaired devices
- [ ] Numbers are unchanged for a device that has only ever had one child — every
      device on DB1 today, so this is the regression test
- [ ] A child moved to a new toy keeps their usage history; a sibling on a used toy
      starts at zero
- [ ] None of the eight dead tables gained a column
- [ ] `rfid_card_tap_log.kid_id` is populated rather than duplicated

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`
