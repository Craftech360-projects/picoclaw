---
status: closed
assignee: claude
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

- [x] Six tables gain `kid_id`, indexed, backfilled — `rfid_card_tap_log` needed
      nothing, it already had the column **and** already populated it
- [x] Rollup writers populate it at write time; raw analytics events gained the
      device lookup they never had
- [x] The parent progress endpoints resolve by child, with the null-guarded MAC
      fallback for unpaired devices
- [x] Numbers are unchanged for a device that has only ever had one child — the
      existing mobile.service suite passes on the same fixtures
- [x] A child moved to a new toy keeps their usage history; a sibling on a used toy
      starts at zero
- [x] None of the dead tables gained a column
- [ ] Verified from the DB after a live session — **deferred, no dev-box deploy**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`


## Resolution

Shipped in `73c54cf7`.

**Much bigger than "one column and one GROUP BY."** The grep found ~110 call sites
across five services. Most are the founder dashboard, which is fleet analytics and
device-oriented on purpose — rekeying it would make it answer a different question
than it asks — so it is untouched. The parent-facing surface was the real target.

That surface turned out to be cheap because 26 of its reads shared one shape,
`mac_address: { in: scope.macAddresses }`, so a single `progressOwnerFilter` replaced
all of them. A parent can own a paired and an unpaired device at once, so the filter
is an OR rather than a branch, and the unpaired arm carries `kid_id: null`.

`rfid_card_tap_log` was already done — column present and populated — so the ticket's
"populate rather than duplicate" criterion needed no work at all.

**Scope grew, and again a test found it rather than a grep.** Three parent-facing
reads resolve their own device list instead of calling `resolveProgressScope`, so
`progressOwnerFilter` never reached them; two existing assertions failed with the
`kid_id` guard missing from the actual call. They now go through `ownerFilterForMacs`,
which resolves the same filter from a MAC list. `getHomepageRecommendations` is
deliberately left device-scoped — it is a suggestion heuristic, not a progress
report, and 009 rewrites part of it anyway.

One schema slip worth recording: the index on `device_analytics_event` was written
against `created_at`, which does not exist on that table — the column is
`event_timestamp`. `prisma validate` caught it before anything ran.

Full suite **1397 passed, 68 suites**. Migration dry-run against the local dev
database and rolled back: all six backfills leave zero rows unattributed on a paired
device, and all eight indexes create.
