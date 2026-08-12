---
status: open
assignee:
---

# 002 — Quiz and Riddler progress follows the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

The tracer bullet: the thinnest complete path from schema through the service to a
live voice session.

`quiz_question_answer` and `riddle_question_answer` are keyed on `device_mac` and
nothing else. Cleared questions and current Level are derived from those rows, never
stored, so a child who changes toys restarts at Level 1 and re-hears everything they
already answered — and a sibling inheriting the toy resumes at the older child's
level.

Add nullable `kid_id` to both tables, backfill it from `ai_device`, and switch reads
to filter on it. Writes set both `kid_id` and `device_mac`; the MAC stays as an audit
column.

Most of the work is already done. `resolveDeviceContext` loads `kid_id` on every
request to pick the Age Band from `kid_profile.birth_date` and then discards it —
this slice stops discarding it. The pure logic in `quiz.logic.js` needs no change at
all: `deriveLevelState`, `countCompletedLevels` and the day gate take a set of
cleared ids and know nothing about how it was fetched.

Reads for an unpaired device fall back to `device_mac`, and that fallback **must**
carry `AND kid_id IS NULL`. Without it, a device unpaired from one child and handed
to a sibling reads by MAC and returns the first child's entire answer log.

On pairing, adopt: stamp rows for that MAC that have no child yet. The `kid_id IS
NULL` guard is what makes it safe to run on every pairing rather than once.

## Acceptance criteria

- [ ] Both answer tables have `kid_id`, indexed on `(kid_id, answered_at)`, backfilled
      from `ai_device` with zero rows left unattributed on DB1
- [ ] Progress, next-questions and the day gate all resolve by child for a paired
      device, and by MAC for an unpaired one
- [ ] The MAC fallback returns nothing for a device that was previously paired to a
      different child — verified with a deliberately constructed row, not by inspection
- [ ] Pairing an unpaired device that has answer rows adopts them; running the
      adoption twice changes nothing the second time
- [ ] A child moved from device A to device B resumes at their existing Level rather
      than restarting at Level 1
- [ ] A sibling paired to a device that previously belonged to another child starts at
      Level 1 with no cleared questions
- [ ] `quiz.logic.js` is unchanged
- [ ] Verified from the DB after **one live Quizzy session and one live Riddler
      session** — answer rows carry the right `kid_id` and the injected prompt block
      shows the expected level. Code review does not close this criterion; the last two
      bank builds both shipped bugs that were invisible in the diff

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — reads filter
  on a column that is null on 18 of 24 devices until pairing is fixed
