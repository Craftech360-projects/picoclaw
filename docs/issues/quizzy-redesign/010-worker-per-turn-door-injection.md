# 010 — Worker injects the Door per turn, and reports it in the MEMO

**Type:** AFK · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §10 Step 3,
[quizzy-doors.md](../../design/quizzy-doors.md).

## What to build

The Doors design needs a Door assignment **per turn**. Today the quiz batch is injected
**once per session** — `RenderQuizQuestions` substitutes `{{QUIZ_QUESTIONS}}` into the
greeting instruction inside `GenerateGreeting`, and that message goes into history once.
There is no refresh path on that placeholder.

**Verified: a per-turn path exists, and it is not the placeholder.** `buildMessages`
runs on every turn and already injects dynamic system directives — the voice directive
and the RFID language lock. The Door directive follows that same pattern.

**One constraint that shapes the implementation:** do not use the existing
insert-after-first-system anchor. The prompt cache breakpoint sits on the static system
block, and OpenAI-side caching is prefix-based via `prompt_cache_key`. The language lock
is safe at that anchor because it is fixed for the whole session; a Door directive
changes every turn, so inserting it there invalidates the cached prefix on every single
turn. **Anchor the Door directive at the tail instead**, after the conversation, where a
per-turn change costs nothing.

Also extend the MEMO contract so the worker reports which Door produced the verdict.
`parseQuizVerdict` ([quiz_state.go:256](../../../pkg/livekit/quiz_state.go)) is the right
place, and its anti-invention guards must keep working.

**Doc fix, in this same change:** [quizzy-doors.md:569](../../design/quizzy-doors.md)
says to verify `RenderQuizQuestions` before committing, and provisional assumption 2
flags per-turn injection as the design's likeliest surprise. Both are now settled —
update them to point at `buildMessages` and record that the assumption held.

## Acceptance criteria

- [ ] Door directive built per turn from the server-supplied Door (009), not chosen by the model
- [ ] Directive anchored at the tail; **not** inserted via the after-first-system anchor
- [ ] Prompt cache hit ratio measured before and after, and shown not to regress
- [ ] Voice directive and language lock behaviour unchanged
- [ ] MEMO carries the Door; `parseQuizVerdict` validates it and rejects a Door the server did not assign
- [ ] Existing anti-invention guards in `parseQuizVerdict` still pass their tests
- [ ] A session with no quiz batch (Cheeko, Nani) injects nothing — verified, since their greetings carry neither placeholder
- [ ] `quizzy-doors.md` §569 and provisional assumption 2 updated to record the verified per-turn path
- [ ] Verified against a real session that the Door escalates within one sitting

## Blocked by

- 009 — Server computes `ask_mode` / Door per question
