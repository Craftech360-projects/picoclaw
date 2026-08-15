---
status: closed
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

- [x] No `3-5`/`6-8`/`9+` row is `active` in either bank
- [x] The CHECK constraint rejects an insert with `age_band = '6-8'`
- [x] No row was deleted from either question table
- [x] Superseded old-band answer rows are gone; rows with no per-age twin survive
- [x] The admin `Correct` column matches the number of questions the child actually
      answered — 11 and 31 for `00:16:3E:AC:B5:38`, not 22 and 52
- [x] `GET /quiz/progress` for a device with pre-migration history still returns its
      lifetime counts
- [ ] A live session in each bank still serves a full ten-question level — **not run
      after the contract**
- [x] Log evidence that no session resolved an old band value during the soak

## Blocked by

- `docs/issues/per-age-banks/002-children-derive-per-age-bank.md`, plus a soak period

## Resolution

Shipped as `prisma/migrations/20260812010000_per_age_banks_contract/migration.sql`.
Preflight found zero old-band answers in the previous 24 hours, so nothing was still
resolving the retired vocabulary.

Applied locally: 90 rows retired per bank, quiz answers 24 → 13, riddle 52 → 31.
Re-applying changed nothing. The verifier passes all four checks on both banks, and
inserting an active `age_band='6-8'` row is now rejected by the constraint.

**The soak was about an hour, not the days this ticket asked for.** Rahul chose to
proceed; recorded here because the ticket's own reasoning argued otherwise.

### The eight-value CHECK was impossible, and this is where it was found

The design's step 3 and this ticket both specified tightening the constraint to the
eight ages. That can never hold: a CHECK covers **every** row in the table, and step 2
deliberately keeps the retired rows rather than deleting them — the answer log's FK is
RESTRICT, and ten dormant riddle answers still point at old-band questions. The first
apply failed on exactly that and rolled the whole transaction back, which is the one
piece of luck here: the delete and the retirement were in the same transaction, so
nothing landed half-done.

The constraint now states the invariant that is actually true — anything **servable**
carries a per-age band, and a retired row may keep the name it was authored under:

```sql
CHECK (age_band IN ('3','4','5','6','7','8','9','10')
       OR (NOT active AND age_band IN ('3-5','6-8','9+')))
```

`000-design.md` §3 is corrected to match. A plain eight-value CHECK would have failed
the same way on production, mid-window.

### Duplicate answers

The double-counting 004 turned up is gone: 11 superseded quiz copies and 21 riddle
copies deleted, matched on the `-a%` code suffix rather than on the device's current
age — so a child whose birthday moved them between banks since the remap still has
their copy found. The ten dormant riddle answers have no twin and survived, as
intended.

Lifetime `correct` now reads 13 for quiz — 11 real answers plus the two scored in the
2026-08-12 live session — and 31 for riddle. The admin page confirms it: the Riddles
tab `Correct` column reads 31, down from 52.

### Verifier

`verify-per-age-banks.js` now asserts the finished state rather than the post-001 one.
Two adjustments were needed, both of which were checks that had quietly become wrong
rather than checks that failed honestly:

- "old band rows still active" is inverted to "retired but still present". Between 001
  and 005 it fails on purpose; that window is what it exists to police.
- The remap check's `mappable > 0` vacuity guard had to go. Post-005 there are
  legitimately no copies left to compare, and requiring some failed the finished state.
  The pre-migration vacuous pass it once guarded is caught more loudly by the first
  check, which finds no per-age content at all.

A new check asserts no superseded copies survive.

Not run: a live voice session after the contract. The service returns quiz level 2 with
8 questions remaining and riddle level 3 with 9, both `band=8`, so the batch path is
intact — but nobody has spoken to it since the old bands went inactive.
