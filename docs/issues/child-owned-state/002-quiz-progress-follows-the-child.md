---
status: closed
assignee: claude
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

- [x] Both answer tables have `kid_id`, indexed on `(kid_id, answered_at)`; the
      backfill is written but **has not run on DB1** — no deploy yet
- [x] Progress, next-questions and the day gate all resolve by child for a paired
      device, and by MAC for an unpaired one
- [~] The MAC fallback returns nothing for a device that was previously paired to a
      different child — asserted at the query level (`kid_id: null` is present in every
      fallback `where`), **not** yet against a constructed row in a real database
- [~] Pairing an unpaired device that has answer rows adopts them — covered; "running
      the adoption twice changes nothing" is guaranteed by the `kid_id IS NULL` guard
      but is **not** exercised by a test that runs it twice
- [x] A child moved from device A to device B resumes at their existing Level rather
      than restarting at Level 1
- [x] A sibling paired to a device that previously belonged to another child starts at
      Level 1 with no cleared questions
- [x] `quiz.logic.js` is unchanged — 0 lines across both commits
- [ ] Verified from the DB after **one live Quizzy session and one live Riddler
      session** — **deferred, no dev-box deploy until asked**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — reads filter
  on a column that is null on 18 of 24 devices until pairing is fixed

## Resolution

Shipped in `72895fef` (cheeko-backend, `feat/child-owned-state`).

One helper carries the decision. `answerScope(context)` returns `{ kid_id }` for a
paired device and `{ device_mac, kid_id: null }` for an unpaired one, and every read
spreads it: cleared lookup, champion replay, day gate, milestone tally, lifetime
counts. `resolveDeviceContext` already loaded the child on every call and discarded
it, so the change is mostly deleting a discard.

Scope grew in one place the ticket did not anticipate. The **admin tools also had to
move**: `set-level` deletes and recreates answer rows, so left MAC-scoped it would
delete nothing a paired device can see and write rows it cannot read back — silently
doing nothing. `reset-day` needed splitting into two raw statements because the
scoped column differs between the two cases.

`recordAnswer` now resolves the context before the insert rather than only inside the
milestone write. That makes it able to fail the answer, which the milestone
deliberately cannot. Chosen on purpose and commented: a row written with the wrong
scope is invisible to every later read and costs the child that question forever,
which is worse than the worker retrying.

13 tests in `quiz.service.child-scope.test.js`, written first and seen to fail (7
red) before implementing, plus 3 adoption tests added to the 001 suite. Full suite:
**1379 passed, 1 failed** — `imagine.test.js`, which fails identically with the
branch stashed.

One thing found and fixed mid-build: renaming the pairing call sites with a
`replace_all` also rewrote the call inside the new wrapper, making it call itself.
Caught by the tests as a stack overflow, not by reading the diff.

**Known gap, deliberate:** `allDeviceProgress` — the admin all-devices listing —
still groups by MAC, so a child who changed toys appears as two rows there. That view
is device-oriented on purpose; changing it belongs to a reporting slice, not this one.
