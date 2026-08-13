---
status: proposed
assignee: unassigned
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

- [ ] Bootstrap returns the recent messages of the character named in the request,
      and the device default when none is named — one test per branch
- [ ] The worker sends its character on the bootstrap call
- [ ] A child with sessions across three characters lists exactly three entries
      from `/kids/:kidId/characters`, with correct counts
- [ ] Two siblings on one parent account, both having talked to Quizzy, each see
      only their own sessions
- [ ] A child whose sessions span two MAC addresses sees all of them in one list
- [ ] A parent requesting another family's `kidId` is refused
- [ ] Message reads reach the child through the session join; no `kid_id` column is
      added to `voice_session_messages`
- [ ] The existing `/agents/:agentId/sessions` response shape is unchanged

## Blocked by

- `001-history-carries-the-speaking-character.md`
