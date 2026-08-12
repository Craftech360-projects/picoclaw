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

No *question* is deleted. The foreign key from the answer log is RESTRICT, and
`kid_learning_progress` rows with old `"3-5 level 1"` topics stay untouched.

**Also drop the superseded answer rows the 001 remap left behind.** Found on the admin
page 2026-08-12: that page's `Correct` column read 22 for a device with 11 real quiz
answers, and 52 for one with 31 riddle answers. The remap *copied* rows onto the per-age
clones rather than moving them — copying is what made 002 a revertable deploy — and the
lifetime tallies in `progress` and `allDeviceProgress` are deliberately not band-scoped,
so each remapped answer is counted twice. Level derivation, the day gate and
`levels_completed` are all unaffected (band-scoped or date-scoped), and no *new* answer
double-counts; this is a one-time inflation for devices that existed before the
migration, and it is permanent until the duplicates go.

Once this issue runs there is nothing left to revert to, so the copies stop being
insurance and become noise:

```sql
DELETE FROM quiz_question_answer a
USING quiz_question old, quiz_question clone, quiz_question_answer twin
WHERE a.question_id = old.id AND old.age_band IN ('3-5','6-8','9+')
  AND clone.code = old.code || '-a' || <device age>       -- same join as the remap
  AND twin.device_mac = a.device_mac AND twin.question_id = clone.id
  AND twin.answered_at = a.answered_at;
```

Only rows that **have** a twin. The 10 dormant riddle answers — old-band content a child
answered at an age whose bank was cloned from elsewhere — have no per-age equivalent, and
deleting those would erase history rather than a duplicate of it.

Before running it, confirm from the logs that no session has resolved an old band since
002 deployed. If one has, that is a bug in the mapping, not a reason to force the
constraint through.

## Acceptance criteria

- [ ] No `3-5`/`6-8`/`9+` row is `active` in either bank
- [ ] The CHECK constraint rejects an insert with `age_band = '6-8'`
- [ ] No row was deleted from either question table
- [ ] Superseded old-band answer rows are gone; rows with no per-age twin survive
- [ ] The admin `Correct` column matches the number of questions the child actually
      answered — 11 and 31 for `00:16:3E:AC:B5:38`, not 22 and 52
- [ ] `GET /quiz/progress` for a device with pre-migration history still returns its
      lifetime counts
- [ ] A live session in each bank still serves a full ten-question level
- [ ] Log evidence that no session resolved an old band value during the soak

## Blocked by

- `docs/issues/per-age-banks/002-children-derive-per-age-bank.md`, plus a soak period
