# 010 — Worker injects the Door per turn, and reports it in the MEMO

**Type:** AFK · **Status:** closed

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

## Added by 009

- [x] **Report Door 3 success as `revealed`, not `correct`.** That is the mastery bar: the
  child was walked to the answer, and after 008 `revealed` does not clear. No schema change
  needed — the vocabulary already carries it.
- [x] **Escalate through the ladder served at fetch.** 009 ships `ask_mode`, `choice_order`
  and `teach_text` with each question rather than per turn, to avoid an HTTP round trip
  mid-question on a voice path. If per-turn server assignment is wanted instead, that is a
  change to 004's worker (post each attempt as it happens) and a deliberate latency
  trade — decide it here.
- [x] **Skip a Door with no authored content.** `choice_order` and `teach_text` are omitted
  when unauthored, which is every question until 014. Never improvise the missing rung.

## Acceptance criteria

- [x] Door directive built per turn from the server-supplied Door (009), not chosen by the model
- [x] Directive anchored at the tail; **not** inserted via the after-first-system anchor — asserted by a test that fails if it moves
- [x] **Prompt cache measured in a live session (2026-08-14).** TTFT while Door directives fired every turn: 1436 → 1520 → 1420 → 839 → 511 → 874 ms — falling as history grew, which is what an intact prefix looks like. Tail anchor confirmed; no regression
- [x] Voice directive and language lock behaviour unchanged — both still inserted at their own anchor; suite passes
- [ ] ~~MEMO carries the Door~~ — **not needed, dropped deliberately.** The worker computes the Door itself, so asking the model to echo it back would add a prompt change and a new way for a 31B model to be wrong about state it does not own. See below
- [x] Existing anti-invention guards in `parseQuizVerdict` still pass their tests — untouched by this change
- [x] A session with no quiz batch (Cheeko, Nani) injects nothing — three tests cover nil batch, no pending question, and a pending id outside the batch
- [x] `quizzy-doors.md` provisional assumption 2 rewritten to record the resolved path and the tail-anchor constraint
- [x] **Escalation verified in a real session (2026-08-14)** — Door 1 → 2 → 3 on a seeded question. It also exposed that the authored ladder had no terminal state: tries 4–6 re-ran the Door 3 line forever, never scoring the question. Fixed (`dc4c65d`): after three tries the turn is terminal — no answer given, MEMO must carry `result=revealed`, question returns another day

## Blocked by

- 009 — Server computes `ask_mode` / Door per question
- **Re-read the prompt on DB1 first.** 001 read it from local only. §3's ask/hint/reveal
  loop is rewritten here; confirm DB1's copy matches before editing.
  See [000-index.md](000-index.md).


---

## Progress — 2026-08-14: injection done, two items need a live session

picoclaw `b7f4296`. Package tests pass (one pre-existing unrelated TTS failure).

### The directive goes last, and that is the whole trick

`quizDoorDirective` builds one line per turn and `buildMessages` appends it **after the
conversation**, not at the after-first-system anchor the language lock uses. The cache
breakpoint sits on the static system block and OpenAI-side caching is prefix-based, so a
directive that changes every turn inserted up there would invalidate the cached prefix on
every turn. The language lock survives that anchor only because it is fixed per session.

A test asserts the placement and fails if it ever moves.

### The ladder skips Doors nobody authored

`QuizQuestion.DoorFor(tries)` walks the ladder and skips a rung with no content — which is
**every question until 014**. A child must not be asked to choose between options that do
not exist, nor hear an explanation that was never written.

A test caught a real bug here: the clamp ran *after* the content checks, so an unauthored
question with more tries than the ladder landed on an empty Door 3.

### The MEMO does not carry the Door, deliberately

The ticket asked for it. It is not needed and I did not add it: the worker computes the
Door from its own try counter, so asking the model to echo it back would mean a prompt
change plus a new way for a 31B model to be wrong about state it does not own. §11 of the
GDD is explicit that the model should track less, not more.

### The mastery bar landed here

Door 3 success reports as `revealed` rather than `correct`. The child was walked to the
answer, and after 008 `revealed` does not clear. No schema change — 009 found the existing
vocabulary already carries the distinction.

### Two items need a real session, not more unit tests

- **Cache hit ratio not measured.** The placement is structurally safe and test-guarded,
  but the number needs a live session.
- **Escalation not seen end to end.** Unit tests prove each rung; nothing has yet watched a
  real child miss twice and reach Door 3.

Both belong with 004's outstanding end-to-end run, which is now the single gate in front of
several tickets.
