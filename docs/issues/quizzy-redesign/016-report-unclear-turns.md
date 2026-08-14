# 016 — The model must report UNCLEAR turns

**Type:** AFK · **Status:** open

## Parent

Found by the first real sessions on 2026-08-14, working [004](004-attempt-log.md) and
[010](010-worker-per-turn-door-injection.md).

## What to build

The worker decides a child missed a question by watching `awaiting=` stay on the same id
across turns: if the question survived, the utterance in between must have been wrong.
That inference is right for answers and wrong for everything else.

Live evidence: **"Can you repeat the question?" was logged as a wrong attempt.** Since two
counted misses now trigger the reveal, a child who simply did not hear would be told the
answer they never got a chance to try for.

A phrase-list filter shipped as a stopgap (`isClarificationRequest`,
[agent_bridge.go](../../../pkg/livekit/agent_bridge.go)). It is a substring match with no
understanding, and its test pins a known false positive rather than pretending there is
none. It covers the phrasings seen live and nothing more.

**The model already knows.** The prompt classifies answers into FIRST_TRY, WITH_HINT,
MISSED and **UNCLEAR**, and says explicitly that unclear speech must not change the score or
the question number. It simply never reports UNCLEAR anywhere — the MEMO carries only
`result=correct|revealed`, so the one participant that can tell "I don't know" from "say
that again" keeps it to itself.

Add an UNCLEAR signal to the MEMO for turns that did not finish judging, and have the
worker skip those turns when counting misses. Then delete the phrase list.

This is the same shape as the bug that caused it: state the model holds and never hands
over. Unlike the try count, though, this genuinely belongs to the model — the server cannot
hear tone, and the worker cannot tell a confused child from a wrong one.

## Acceptance criteria

- [ ] MEMO carries an explicit marker for a turn the model judged UNCLEAR
- [ ] The worker skips those turns when counting misses toward the reveal threshold
- [ ] A clarification request does not appear as a `wrong` attempt row
- [ ] A genuine wrong answer still counts, including "I don't know"
- [ ] `isClarificationRequest` and its phrase list are deleted, not left as a second path
- [ ] Prompt change goes through the backup-and-diff procedure; the marker is additive so an
      older worker ignores it
- [ ] Verified in a real session: ask for a repeat twice, then answer — the reveal must not
      have fired early

## Blocked by

None - can start immediately. Touches the prompt, so re-read it first per
[000-index.md](000-index.md).
