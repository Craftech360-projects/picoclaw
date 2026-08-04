# 004 — Worker fetches the batch and injects {{QUIZ_QUESTIONS}}

**Type:** AFK · **Status:** ready
**Spec / Plan:** as 001 (plan Tasks 5–6)
**Repo:** picoclaw (branch `feat/quizzy-question-bank`)

## What to build

The worker side of the fetch path, end to end: at session start, when the character's greeting prompt contains `{{QUIZ_QUESTIONS}}` (character-agnostic gate — Cheeko/Nani are untouched because their prompts lack the placeholder), call 002's endpoint with the device MAC and render the batch into the greeting via a new placeholder renderer alongside the existing `{{TODAY_PLAN}}` mechanism.

Rendering contract (decision-rich, from the plan):

```
## Today's Quiz Questions (Level 3, band 6-8)
Ask ONLY these questions, in order, one per turn. Never invent a question.
1. (id=482) How many legs does a spider have? — Answer: eight (also accept: 8)
```

- `replay: true` → add champion-rounds framing line.
- Fetch failed / nil batch → replace placeholder with an explicit no-quiz instruction: do NOT run a scored quiz, do NOT invent questions, offer free chat. (Spec principle: the LLM invents unscored content only.)
- Batch and a verdict-reporter callback ride the bridge config for issue 005.

## Acceptance criteria

- [ ] Go tests: renderer (numbered list, replay framing, nil-batch no-quiz text, no-placeholder passthrough) and fetch client (httptest, envelope unwrap, string ids parsed) pass
- [ ] `go build ./...` clean; `go test ./pkg/livekit/` shows only the known pre-existing failure
- [ ] On the dev box (deploy boundary: dev only), a session against a scratch greeting containing the placeholder logs `Quiz batch fetched` with level/band/count and the greeting message carries a seeded question
- [ ] A session for a character without the placeholder makes no quiz API call (verify via logs)
- [ ] Committed on the picoclaw branch

## Blocked by

- 002 (endpoint must exist on the dev box)
