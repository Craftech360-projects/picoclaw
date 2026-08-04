# 004 — Worker fetches the batch and injects {{QUIZ_QUESTIONS}}

**Type:** AFK · **Status:** closed (dev-box E2E deferred to 006)
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

- 002 (endpoint must exist on the dev box) — closed

## Resolution

Shipped in `9c7369f` (picoclaw). Built in parallel with 002 against the frozen response
contract; URL construction verified compatible afterwards (base already carries `/toy`).

`pkg/livekit/quiz_bank.go` exports `QuizQuestion`, `QuizBatch`, `FetchQuizBatch`,
`PostQuizAnswer`, `RenderQuizQuestions`, `NewQuizAnswerReporter` — HTTP shape copied from
`doFetchManagerCharacterSession`. 12 table-driven tests: rendering (ids, alternates,
replay framing, nil batch no-quiz text, placeholder-free passthrough) and fetching
(envelope unwrap, string→int64 ids, unparseable id dropped, HTTP and envelope errors,
empty MAC short-circuit). `go test ./pkg/livekit/` shows only the known pre-existing
`TestSynthesizeAndPlayLogsTTSProviderType` failure.

The fetch is gated on `strings.Contains(personaGreeting, "{{QUIZ_QUESTIONS}}")`, so
Cheeko, Nani and every other character make no quiz call at all. Failure is warn-and-
continue with a nil batch, which renders the explicit do-not-invent instruction.

`NewQuizAnswerReporter` (one retry, then log-and-drop) is wired onto the bridge but
unused until SUB-005. Dev-box deploy and live-session checks deferred to SUB-006.

`go build ./...` fails only on `mautrix/crypto/libolm` (missing cgo headers on this
Windows box) — reproduced on a stashed clean tree, not caused by this change.
