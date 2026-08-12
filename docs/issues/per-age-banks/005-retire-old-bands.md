---
status: open
assignee: claude
---

# 005 — Retire the old bands

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

The contract half of the migration, and the point after which rolling back 002 is no
longer a one-command deploy revert. Deliberately its own slice so it lands after a real
soak — at least a few days of live sessions across more than one age.

For both banks: set `active = false` on every `3-5`/`6-8`/`9+` row, then tighten the
`age_band` CHECK to the eight single ages only.

Nothing is deleted. The foreign key from the answer log is RESTRICT and the log is
append-only history — a child's pre-migration answers must stay readable, and
`kid_learning_progress` rows with old `"3-5 level 1"` topics stay untouched.

Before running it, confirm from the logs that no session has resolved an old band since
002 deployed. If one has, that is a bug in the mapping, not a reason to force the
constraint through.

## Acceptance criteria

- [ ] No `3-5`/`6-8`/`9+` row is `active` in either bank
- [ ] The CHECK constraint rejects an insert with `age_band = '6-8'`
- [ ] No row was deleted from either question table or either answer table
- [ ] `GET /quiz/progress` for a device with pre-migration history still returns its
      lifetime counts
- [ ] A live session in each bank still serves a full ten-question level
- [ ] Log evidence that no session resolved an old band value during the soak

## Blocked by

- `docs/issues/per-age-banks/002-children-derive-per-age-bank.md`, plus a soak period
