---
status: closed
assignee: claude
---

# 002 — Reads scope by child and character

## Parent

`docs/issues/chat-history-attribution/000-design.md`

## Why now

`001` makes `agent_id` truthful. Every read that consumes it is currently written
against the old, untruthful value, so `001` alone moves the breakage rather than
fixing it. The worst case is device bootstrap, which filters
`agent_id: device.agent_id` (`agent.service.js:1841`) — after `001`, a Quizzy
session bootstraps and gets **Cheeko's** messages back.

## What to build

### A. Bootstrap reads the running character, not the device default

`getDeviceBootstrap` takes the character from the caller instead of assuming the
device's default:

- accept an optional `agentId` (or `characterId`) query parameter on
  `GET /agent/device/{mac}/bootstrap`
- `recentMessages` filters `{ voice_sessions: voiceSessionScope(device, mac), agent_id: <that character> }`
- when the parameter is absent, keep today's `device.agent_id` behaviour so
  existing callers do not change shape

The picoclaw worker passes it: `ManagerAPIBackend` already holds `AgentID`
(`pkg/session/manager_api_backend.go:27`) and builds the bootstrap URL at
`:225` — append the parameter when it is non-empty.

Same treatment for the summary read if it turns out to be device-scoped rather
than session-scoped; check `voice_session_summaries` in the bootstrap before
assuming.

### B. Parent-app endpoints keyed by child

The two endpoints the app has today are account-scoped, not child-scoped:

- `GET /api/mobile/agents/:agentId/sessions` (`mobile.routes.js:665`)
- `GET /api/mobile/agents/:agentId/chat-history/:sessionId` (`:671`)

They resolve through `getAgentSessions` / `getChatHistory`
(`agent.service.js:314`, `:374`), both `where: { agent_id }` only. Two siblings on
one account get each other's sessions, and a child who changed toys gets nothing
new — the queries never mention the child.

Add child scope. Three endpoints, all Firebase-authed and passed through
`resolveProgressScope` (the same ownership guard the progress screens use, so a
parent cannot read another family's child):

| Endpoint | Returns |
|---|---|
| `GET /api/mobile/kids/:kidId/characters` | characters this child has talked to: `agentId`, `agentName`, `sessionCount`, `lastSessionAt` |
| `GET /api/mobile/kids/:kidId/characters/:agentId/sessions` | that child's sessions with that character: `sessionId`, `startedAt`, `endedAt`, `messageCount`, `summary` — paginated |
| `GET /api/mobile/kids/:kidId/sessions/:sessionId/messages` | the transcript: `role`, `content`, `createdAt` — cursor-paginated, ownership checked via the session's `kid_id` |

All three filter `voice_sessions.kid_id = :kidId` and join messages through the
session — never `mac_address`. That is what makes a toy swap invisible: the rows
follow the child, and the device is only a column.

Keep the two existing `/agents/:agentId/...` endpoints working. They become
account-wide views, and after `001` they are at last per character.

## Acceptance criteria

- [x] Bootstrap returns the recent messages of the character named in the request,
      and the device default when none is named — one test per branch
- [x] The worker sends its character on the bootstrap call
- [x] A child with sessions across three characters lists exactly three entries
      from `/kids/:kidId/characters`, with correct counts
- [x] Two siblings on one parent account, both having talked to Quizzy, each see
      only their own sessions — asserted on the where clause, which is where the
      separation lives; no DB in the unit suite
- [x] A child whose sessions span two MAC addresses sees all of them in one list —
      same boundary: the query names `kid_id` and no `mac_address` at all
- [x] A parent requesting another family's `kidId` is refused
- [x] Message reads reach the child through the session join; no `kid_id` column is
      added to `voice_session_messages`
- [x] The existing `/agents/:agentId/sessions` response shape is unchanged — the
      code is untouched and the integration sweep that calls it still passes
- [ ] Verified against a real app or device — **not run, no device and no deploy
      from this session**; joins 001's live criteria in `004`

## Blocked by

- `001-history-carries-the-speaking-character.md`

## Resolution

Shipped in `f2c6667` (picoclaw) and `637e936c` (manager-api). Full jest suite
**1495 passed, 76 suites**; `pkg/session` green. `pkg/livekit`'s
`TestSynthesizeAndPlayLogsTTSProviderType` fails on this branch with these files
stashed too — pre-existing, not from here, same one `001` recorded.

**The bootstrap's summary read did not need the exemption the ticket offered.**
The ticket said to give summaries "the same treatment *if* it turns out to be
device-scoped rather than session-scoped". It is session-scoped — `006` already
moved it — but it *also* filtered `agent_id: device.agent_id`, so it carried the
identical bug through a different clause. `recentSessions` did too, and the ticket
does not mention it at all. All three now read one resolved `agentId`, which is
`options.agentId || device.agent_id`; leaving one of them on the device default
would have made the three halves of one bootstrap disagree about who was talking.
Note that this makes summaries per character in the bootstrap, which `000` point 4
wants shared — that sharing lives in `MEMORY.md` and is `003`'s to keep, untouched
here.

**What did not change: the `agent` block.** The bootstrap still resolves the
persona (system prompt, voice, models) from `device.agent_id`, so a Quizzy
bootstrap gets Quizzy's history beside Cheeko's prompt. The worker takes its
persona from room metadata, not from here, and widening this would have changed
the response's `agent`/`device.agentId` for every existing caller. Named, not
fixed.

**The ownership guard grew one option rather than gaining a twin.**
`resolveProgressScope` checked a named *toy* against the caller's account; it now
checks a named *child* the same way, and returns that child as the scope. The
alternative — asserting the kid appears in `scope.kidIds`, which is derived from
devices — refuses a child who is not currently paired to a toy, which is exactly
the child this whole design is for. Existing callers pass no `kidId` and are
unaffected; a caller that passes one to a progress endpoint would now be scoped
by it instead of having it ignored.

**Three routes, all Firebase-authed, keyed on the child**, filtering
`voice_sessions.kid_id` with no `mac_address` anywhere in them. Messages reach the
child through the session relation — no column added, per `000`'s invariant — so
another child's `sessionId` reads as an empty transcript rather than a leak.

**Not verified live.** Every criterion above is proven at the query boundary with
prisma mocked, in this repo's existing style. Nothing here has met a device or the
real app; that verification is `004`'s.

One process note: this repo's working tree was shared with the agent doing `003`
while both were in flight. Every commit here was scoped to explicit paths, and
`003`'s code is untouched by them — but the two runs did interleave: `003`'s
`94be8eb` landed between this ticket's code and doc commits, and one line of
`003`'s own resolution rode along in the doc commit below. Nothing was lost;
worth knowing before reading the history as a sequence.
