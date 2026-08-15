# 012 — Anti-trap: `days_on_level` derived from the answer log

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §4 M3, §10 Step 4.

## What to build

The mastery rule (008) can trap a child: a question they cannot answer keeps reopening,
and they stall on one level indefinitely. M3 caps that at **3 days on a level**, after
which the child advances regardless.

**`days_on_level` must derive from `quiz_question_answer`, not from the attempt log.**
The attempt log write can fail — it is deliberately non-blocking (004). If the cap
depended on it, a run of failed attempt writes would silently mean the cap never fires,
and the child stays trapped with no error anywhere. Derive the gate from the outcome
log, which is the write that must succeed anyway.

Keep ADR-0005's rule: **progress is derived, never stored.** No `days_on_level` column.
Add the spaced-repetition pool as a query, not a table, if you can.

Interaction with 009: spaced-repetition items are bonus-only and never block, so they
must not count toward days-on-level either.

## Acceptance criteria

- [x] `days_on_level` computed from `quiz_question_answer` on read; no new column, no stored counter
- [x] Cap set to 3 days per the locked design decision — `ANTI_TRAP_DAY_CAP`
- [x] Child on a level for 3 days advances even with unmastered questions remaining — verified live
- [x] Cap verified to still fire when the attempt log has no rows for the period — the live run asserts zero attempt rows throughout
- [x] Bonus / spaced-repetition items excluded from the day count — bonus items are appended outside `selectedIds` and never enter the cleared set
- [x] Day counting matches the existing day-gate's day boundary — both use server-local midnight, in the same function
- [x] Advancing via the cap is distinguishable — `anti_trap_advanced` on the response, plus a warn-level log line naming the device, days and level
- [x] Test covers the trap case: a child who never masters a question still advances on day 3, and the question they were stuck on comes with them

## Blocked by

- 004 — Attempt log
- 009 — Server computes `ask_mode` / Door per question


---

## Findings — 2026-08-14: done

`manager-api-node` `d2bd581d`. 605 unit tests pass; verified live against local.

### Derived from the answer log, and that choice is the ticket

The cap counts distinct local days in `quiz_question_answer`. It deliberately does **not**
read the attempt log: that write is allowed to fail (ADR-0009), so a cap reading it would
silently stop firing exactly when it failed — and the child it exists to rescue would stay
stuck with nothing in the logs to explain why. The live run asserts the cap fires with
**zero** attempt rows present, which is the case that would otherwise rot unnoticed.

Day boundary is server-local midnight, matching the day gate a few lines below it in the
same function. Two definitions of "day" in one file would eventually disagree by one, and
the disagreement would show up as a child gaining or losing a day.

### The trapped questions become the spaced-repetition pool

009 carried a bonus-only flag with nothing to attach to. This is what it attaches to: when
the cap fires, the questions the child did not master ride along as **bonus items** rather
than being abandoned. Flagged, appended outside the selection set, never counted towards
clearing a level, and a missed bonus simply recycles.

A query, not a table — ADR-0005's rule holds.

### Verified live

- three days on a level advances the child, and carries the question they were stuck on
- two days does **not** trip it — the cap is 3, not "a few"
- six answers in one day counts as **one** day, not six
- the whole thing fires with zero attempt rows

Backdated rows rather than deleting and rewriting: the answer log is evidence.

### One consequence worth stating

`anti_trap_advanced` says a child moved on without mastering the level. That is the signal
worth watching once real children are playing — if it fires often, the mastery bar is too
hard or the bank's early levels are, and the redesign needs tuning rather than more
enforcement.
