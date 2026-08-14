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

- [ ] Door 3 serves authored `teach_text` from the question row; no improvised explanation when text exists
- [ ] Behaviour defined and tested for a question whose `teach_text` is null
- [ ] `questionTextMatchesBank` re-verified against **real** Door 3 transcripts, with the sample size recorded
- [ ] False-reject rate measured: legitimate Door 3 verdicts that the guard drops
- [ ] False-accept still zero: no invented question reaches the database in the test corpus
- [ ] Multilingual judging rule extended per 001's finding, or explicitly recorded as not needed
- [ ] Door 3 does not clear a question (mastery bar stays at Door 1 or 2, per 009)
- [ ] Any guard relaxation justified by transcript evidence in a comment, not by hand-written examples

## Blocked by

- 007 — `teach_text` in the importer
- 010 — Worker injects the Door per turn
