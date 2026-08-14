# 016 — The model must report UNCLEAR turns

**Type:** AFK · **Status:** built, pending one session check

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

- [x] MEMO carries `unclear=yes` for a turn the model judged UNCLEAR
- [x] The worker skips those turns when counting misses toward the reveal threshold
- [x] A clarification request does not appear as a `wrong` attempt row
- [x] A genuine wrong answer still counts — the test sends **identical words** down both paths, separated only by the model's judgement
- [x] `isClarificationRequest` and its phrase list are **deleted**
- [x] Prompt change went through backup-and-diff; 13,311 → 13,835 chars, 1 row, verified by re-dump. Additive, so an older worker ignores it
- [ ] **Verified in a real session — outstanding.** Ask for a repeat twice, then answer; the reveal must not fire early

## Blocked by

None - can start immediately. Touches the prompt, so re-read it first per
[000-index.md](000-index.md).


---

## Done — 2026-08-14 (picoclaw + `manager-api-node` `3147db05`)

### The model reports, the worker stops guessing

`unclear=yes` on the MEMO. The prompt has always classified UNCLEAR and never reported it,
so the worker inferred a wrong try from "the same question is still pending" — right for
answers, wrong for everything else.

The prompt now says two things explicitly: asking to hear the question again is **unclear,
not wrong**, and an attempted answer — however wrong — must **not** set the flag. Without
the second half a model looking to be helpful would mark every struggling turn unclear and
the reveal would never fire.

### The phrase list is gone, not kept alongside

`isClarificationRequest` and its thirteen phrases are deleted. It matched substrings with no
understanding, and its own test pinned the failure: *"a pardon is a forgiveness"* read as a
request to repeat.

The replacement test is the argument for the whole ticket — it sends the **identical
sentence** through both paths and gets different answers, because the only thing separating
them is the model's judgement. No word list can do that.

`memoFlagIsYes` accepts yes/true/1 in any casing: a 31B model will not be consistent, and
losing the flag to a capital letter would silently restore the bug.

### Coupling worth knowing

Worker and prompt must move together. On a database still carrying the old prompt the flag
never arrives, so **every** turn counts as an attempt again — the pre-016 behaviour, not a
crash, but the bug returns. DB1 and prod still have the old prompt; 008, 015 and 016 all
need applying there.

### Outstanding

One live check: ask for a repeat twice, then answer. The reveal must not have fired early,
and `question_attempt` must hold one row rather than three.
