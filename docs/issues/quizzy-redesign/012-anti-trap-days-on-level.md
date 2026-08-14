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

- [ ] `days_on_level` computed from `quiz_question_answer` on read; no new column, no stored counter
- [ ] Cap set to 3 days per the locked design decision
- [ ] Child on a level for 3 days advances even with unmastered questions remaining
- [ ] Cap verified to still fire when the attempt log has no rows for the period
- [ ] Bonus / spaced-repetition items excluded from the day count
- [ ] Day counting matches the existing day-gate's day boundary — same timezone rule, verified against it
- [ ] Advancing via the cap is distinguishable in the data from advancing by mastery
- [ ] Test covers the trap case end-to-end: a child who never masters a question still advances on day 3

## Blocked by

- 004 — Attempt log
- 009 — Server computes `ask_mode` / Door per question
