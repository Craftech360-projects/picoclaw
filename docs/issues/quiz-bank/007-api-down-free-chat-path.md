# 007 — Verify the API-down free-chat path

**Type:** HITL · **Status:** open
**Carried from:** [006](006-quizzy-prompt-cutover-e2e.md) — the one acceptance criterion never exercised

## Why this matters

Every other failure mode in the bank design has now been seen in a real session. This one has
not, and it is the one that decides what a child hears when the Manager API is unreachable.

The code path is deliberate: `FetchQuizBatch` failing must never fail the session, and
`quizQuestionsBlock(nil)` renders an explicit instruction rather than an empty list —

> The question bank is unavailable right now. Do NOT run a scored quiz and do NOT invent
> questions. Offer free chat instead, and tell the child new questions are coming soon.

If that instruction is not obeyed, the failure is silent and expensive: Quizzy invents
questions, the child answers them, nothing is logged, and tomorrow the real bank re-serves
questions they already answered. That is precisely the behaviour ADR-0005 exists to prevent —
it would look like the bank was never built.

`WriteQuizBankState` also clears any stale `quiz_bank.md` on a failed fetch, so yesterday's
list cannot be served as today's. That deletion should be confirmed too.

## Acceptance criteria

- [ ] With `manager-api` stopped on the dev box, start a Quizzy session on a test device
- [ ] Worker logs `Quiz batch fetch failed; scored quiz disabled this session`
- [ ] Quizzy offers free chat and speaks the "new questions are coming soon" framing
- [ ] **Zero** scored questions asked — check the transcript in `voice_session_messages`,
      not just the absence of rows
- [ ] **Zero** new `quiz_question_answer` rows for that device during the session
- [ ] `memory/state/quiz_bank.md` is removed, not left holding a previous session's list
- [ ] Restart `manager-api`; the next session serves the correct level and nothing was
      double-counted

## How to run it

Stop the API rather than breaking the URL — a wrong URL exercises DNS/connection-refused,
which is a different path from a service that is down:

```bash
ssh root@64.227.170.31 'pm2 stop manager-api'
# ... run one session on a test device, then:
ssh root@64.227.170.31 'pm2 start manager-api'
```

Note the worker's fetch timeout is 3s, so the session should start only marginally slower —
if it hangs noticeably longer than that, the timeout is not being honoured and that is itself
a finding.

## Watch out for

The persona pull also goes to the Manager API. With it stopped, the session may fail earlier
than the quiz fetch and never reach the path under test — in which case this needs the API up
but the `/quiz/*` routes specifically failing (e.g. a temporary 500) rather than the whole
service down. Confirm which failure actually occurred before recording a pass.
