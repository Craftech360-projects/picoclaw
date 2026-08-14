# 015 — M4 Wonder Question, shipped alone

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §4 M4, §6a-2, §10 Step 5.

## What to build

The Wonder Question is the intrinsic engine: at the end of a session Quizzy asks the
child something open and unscored, and remembers it for next time.

**Ship it first and alone.** It is the cheapest change in the engagement layer with the
largest predicted effect — and shipping it by itself is the only way to learn whether
that prediction was right. Bundled with M5/M6/M7, the signal is unreadable.

One new table, `kid_wonder_question` (§6a-2). Everything else in the engagement layer
reuses `analytics_streaks` and `kid_learning_progress.metadata` — do not add tables for
those here.

**Non-negotiable: no reward state in `memory/state/`.** The 48h prune
([quiz_state.go:26](../../../pkg/livekit/quiz_state.go)) deletes it. §6a-2 records that
this nearly shipped as a bug: a reward the child earned would silently vanish two days
later, which is worse than never having offered it.

The Wonder Question is unscored and must never gate progression — it is one of the
parts the model is allowed to improvise (§11).

**Open question to decide before shipping (§13 Q4):** does the Wonder Question need a
parent-visible log? It is strong for retention, but it is a record of a child's private
curiosity. Decide deliberately and record the choice; do not let the default fall out of
whatever is easiest to build.

## Acceptance criteria

- [x] `kid_wonder_question` table added; no other new tables
- [x] Persisted against the child where one is known, falling back to the device with `kid_id IS NULL` — mirroring `answerScope`, so a shared toy does not hand one child's wondering to another
- [x] Prior wonder question surfaces in a later session — returned as `wonder_question` in the batch and rendered ahead of the quiz block
- [x] Survives more than 48 hours — it is a database row, not `memory/state/`, which is the exact failure §6a-2 warns about
- [x] Nothing written under `memory/state/`
- [x] Unscored: verified no answer row, no attempt row, and no level movement
- [x] Parent-visibility decision made — **not visible**, see below
- [x] M5, M6 and M7 explicitly **not** included in this change
- [ ] **Verified against a real session across two days — outstanding.** Needs the prompt to emit `wonder=` in the MEMO; see below

## Blocked by

- 004 — Attempt log


---

## Progress — 2026-08-14: built and verified locally, one prompt line left

`manager-api-node` + picoclaw `4aefced`. 599 + package tests pass.

### The decision: not parent-visible

Stored, used to open the next session, **not** on the parent dashboard.

It is a record of a child's private curiosity. A child who knows their wondering is
reported may wonder differently, or stop saying the odd ones out loud — which would cost
exactly the thing the mechanic exists to create. Showing it later is additive; un-showing
it after parents have seen it is not.

For the same reason the question text stays **out of the logs**. A log line is a second
place it would have to be protected, and it buys nothing: the character count is enough to
debug with.

### How it works

- `POST /quiz/wonder` saves it; `next-questions` returns the previous one as
  `wonder_question`, null for a child who has never been left one
- The worker renders it as its own opening beat ahead of the quiz block, explicitly marked
  unscored, and captures the new one from a `wonder=` MEMO field
- Flushed at teardown beside the attempt flush — synchronous and idempotent

**Read by child, falling back to device with `kid_id IS NULL`**, mirroring `answerScope`.
Without that guard an unpaired toy handed to a sibling would open by asking about the
previous child's curiosity.

**The read is non-fatal.** If it fails the quiz still runs; the cost is a warm opening
line, not a session.

One thing found while wiring it: capture had to read the MEMO **before** the verdict guard.
Behind it, the Wonder Question would have been silently dropped whenever the quiz path was
inactive — two unrelated things coupled by an early return.

### The prompt patch — applied to local 2026-08-14

`manager-api-node/scripts/patch-quiz-prompt-015.js`. Two additive edits, 12,638 → 13,311
chars, 1 row changed, verified by re-dump. Backup in `/tmp/p015`.

1. **The closing beat**, after the score is announced: one open question with no right
   answer, asked warmly, and the conversation ends there. Spelled out as **not** a quiz
   question — not judged, not scored, not corrected, never affects the Daily Ten. Without
   saying that explicitly, a model told to ask a question reaches for its quiz reflexes and
   marks the child's answer, which would turn the one unscored moment into another test.
2. **`wonder=` on the completion MEMO**, which is how the worker receives it.

Dry-run by default, and the `UPDATE` is guarded on the exact prior text so a prompt that
moved since the backup updates nothing rather than clobbering another edit.

**DB1 and prod still have the old prompt.** Same patch applies cleanly there when promoting.

### Remaining: one real two-day test

Play a session, hang up, come back the next day. Quizzy should close by wondering something
aloud and open the next session by remembering it. `kid_wonder_question` should hold exactly
one row per session that ended properly, and `quiz_question_answer` should be untouched by
any of it.
