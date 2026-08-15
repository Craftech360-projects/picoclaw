# Chat history API — what changed on the server

> Manager API (`manager-api-node`), shipped 2026-08-13 on the dev box.
> Companion doc: `chat-history-app-integration.md` — request/response models and
> the app-side changes.
>
> Context: `docs/issues/chat-history-attribution/000-design.md`.

All paths below are relative to the API root, which includes the `/toy` context
path — e.g. `http://64.227.170.31:8002/toy/api/mobile/kids/15/characters`.

Every response is wrapped by the standard envelope. **`code: 0` means success**,
even on HTTP 200 — a non-zero `code` is a failure and must be treated as one:

```json
{ "code": 0, "msg": "success", "data": { } }
```

---

## 1. Why anything changed

Chat history was being stored under the wrong character. The voice worker never
sent the character it was running, so the Manager substituted
`ai_device.agent_id` — the device's **default** character. Measured on the dev
database before the fix: **1635 messages over 30 days, every one attributed to
`Cheeko`**, while `Nani`, `Masti`, `Quizzy` and `Riddler` were in daily use.

Fixing attribution made a second problem visible: the endpoints the app had were
keyed on the **character alone**, with no child in the query. Two siblings on one
account read each other's sessions, and a child who moved to a new toy read none
of their own.

---

## 2. New endpoints — browse by child

Three read endpoints, all **Firebase-authenticated** (`Authorization: Bearer
<idToken>`) and ownership-checked against the caller's account. All of them filter
on `voice_sessions.kid_id` and **never** on MAC address, which is what makes a
child's history follow them to a different toy.

### `GET /api/mobile/kids/:kidId/characters`

The characters this child has actually talked to.

```json
{ "code": 0, "data": [
  { "agentId": "0cce451a-…", "agentName": "Cheeko", "sessionCount": 73, "lastSessionAt": "2026-08-13T08:40:00.000Z" },
  { "agentId": "bd083d4d-…", "agentName": "NANI",   "sessionCount": 1,  "lastSessionAt": "2026-08-13T15:05:00.000Z" }
]}
```

Sorted by `lastSessionAt`, newest first. `agentName` may be `null` if the agent
row has since been deleted.

**One entry per character, not per agent row.** An account can hold several rows
for one character; duplicates are merged here and their `sessionCount`s summed.
`agentId` is the most recently used of them and is always safe to pass to the next
endpoint.

### `GET /api/mobile/kids/:kidId/characters/:agentId/sessions`

That child's conversations with that character. Page-based.

Query: `page` (default 1), `limit` (default 20, **max 100**).

```json
{ "code": 0, "data": {
  "total": 73,
  "list": [
    { "sessionId": "0e6bb044-…_00163E7A11C4_conversation",
      "startedAt": "2026-08-13T15:05:00.000Z",
      "endedAt": "2026-08-13T15:11:00.000Z",
      "messageCount": 5,
      "summary": "Kahani Nani progressed the story of the clever monkey." }
  ]
}}
```

`endedAt` is `null` for a session that never closed cleanly. `summary` is `null`
until the worker writes one at session end.

Passing **any** `agentId` belonging to a duplicated character returns the sessions
of all of them, so the list matches the count shown on the previous screen.

### `GET /api/mobile/kids/:kidId/sessions/:sessionId/messages`

The transcript. **Cursor-based**, not page-based — the cursor is the message
`sequence`, not an offset.

Query: `cursor` (default 0, exclusive), `limit` (default 100, **max 500**).

```json
{ "code": 0, "data": {
  "sessionId": "0e6bb044-…_conversation",
  "messages": [
    { "sequence": 1, "role": "user",      "content": "tell me a story", "createdAt": "2026-08-13T15:05:10.000Z" },
    { "sequence": 2, "role": "assistant", "content": "Once upon a time…", "createdAt": "2026-08-13T15:05:14.000Z" }
  ],
  "hasMore": false,
  "nextCursor": null
}}
```

`role` is `"user"` or `"assistant"`. Ordered by `sequence` ascending. To page,
send back the `nextCursor` you were given; stop when `hasMore` is `false`.

A `sessionId` belonging to a different child returns an **empty** transcript
rather than an error — the ownership filter is part of the query.

### Ownership and errors

| condition | status |
|---|---|
| `kidId` not owned by the caller | `404 Kid not found` |
| `kidId` malformed | `400` |
| no/invalid Firebase token | `401` |

---

## 3. Changed behaviour on existing endpoints

### `POST /api/mobile/agents` is now idempotent per character

**Previously** every call inserted a new row. The app creates one agent per toy
activation with a self-uniquified name (`Cheeko 2`, `Cheeko 3`), and the server
normalises that suffix away so the persona resolves and the name is not spoken
aloud as "Cheeko two". The result was one row per activation — and per retry after
a failed bind. One account accumulated **fourteen** rows called `Cheeko`.

**Now** the account's existing row for that character is returned instead. Same
200, same `data` (the agent id), but the id may be one you have seen before.

> An agent is a **character**. A device is a **toy**. Two toys running Cheeko share
> one agent row, and their history stays separate because it is keyed on the
> child. See §5 for the consequence.

### Binding failures return 4xx instead of 500

`POST /api/mobile/agents/:agentId/bind/:deviceCode` threw plain errors, so every
user mistake surfaced as a `500`.

| case | before | now |
|---|---|---|
| wrong/expired 6-digit code | 500 | **400** |
| device MAC not found | 500 | **404** |
| agent not found / not yours | 500 | **404** |
| toy already bound to another account | 500 | **409** |
| bad code format | 500 | **400** |

### `GET /agent/device/:mac/bootstrap` accepts `agentId`

Service-key endpoint used by the voice worker, documented here because it changed
shape. It takes an optional `agentId` query parameter and scopes
`recentMessages`, `recentSessions` and the summary to that character. Without it,
behaviour is unchanged — the device's default character.

### Unchanged

`GET /api/mobile/agents/:agentId/sessions` and
`GET /api/mobile/agents/:agentId/chat-history/:sessionId` keep their exact
response shape. They are account-wide views — a character, whoever spoke to it —
and after the attribution fix they are at last genuinely per character. The app
should stop using them for the per-child screens (see the integration doc), but
nothing breaks if it does not.

---

## 4. What the stored data now means

- `voice_sessions.agent_id` / `voice_session_messages.agent_id` — the character
  that actually spoke, not the device default. **Correct from 2026-08-13 13:46
  IST on dev.** Earlier rows are all attributed to the device default and cannot
  be repaired: nothing on a message row records who spoke.
- `voice_sessions.kid_id` — the child, taken from the device's pairing at the time
  of the session. `null` for an **unpaired** toy, which is legitimate: a toy out
  of the box has no child yet, and its sessions will not appear under any child.

---

## 5. Two consequences worth knowing before you build against this

**Character settings are per account, not per child.** `ai_agent` carries
`language`, `tts_voice_id`, `elevenlabs_voice_id` and persona fields. With one row
per character, setting kid A's Cheeko to Hindi also changes kid B's. Before the
deduplication, duplicate rows accidentally allowed per-toy settings; that was a
side effect of the bug, not a feature. If per-child character settings are wanted,
they need their own `(kid_id, agent_id)` override table — not duplicate agent rows,
which is what broke the history view in the first place.

**One agent can now have several devices.** `GET /api/mobile/agents/:agentId/devices`
may return more than one toy. Any UI that assumes "one agent = one toy" needs to
read the toy list from devices instead.
