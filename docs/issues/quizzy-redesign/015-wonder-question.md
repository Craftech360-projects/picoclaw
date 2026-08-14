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

- [ ] `kid_wonder_question` table added; no other new tables
- [ ] Question asked at end of session and persisted against the child, not the device
- [ ] Prior wonder question surfaces in a later session
- [ ] Survives more than 48 hours — verified, since this is the exact failure §6a-2 warns about
- [ ] Nothing written under `memory/state/`
- [ ] Unscored: no verdict row, no effect on level, mastery, or the day gate
- [ ] Parent-visibility decision made and recorded before ship
- [ ] M5, M6 and M7 explicitly **not** included in this change
- [ ] Verified against a real session across two days

## Blocked by

- 004 — Attempt log
