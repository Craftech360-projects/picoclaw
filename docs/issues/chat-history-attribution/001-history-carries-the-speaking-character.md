---
status: in-progress
assignee: claude
---

# 001 — Chat history must carry the character that actually spoke

## Parent

`docs/issues/chat-history-attribution/000-design.md`

Every session's transcript is stamped with the device's **default** character
(Cheeko on the test devices) no matter who the child talked to — Masti, Riddler,
Quizzy, Nani, Mittu all land under Cheeko in `voice_session_messages.agent_id`.

## The chain, with the evidence

**1. The gateway never sends `agent_id`. It sends `character_id`.**

`core/mem0-integration.js:32` (`buildDispatchMetadata`) is the only writer of
room metadata, and its key set is:

```js
device_mac, device_uuid, kid_id, character, character_id, language,
sarvam_voice_id, elevenlabs_voice_id, child_profile, session_*, ...
```

`character_id` is `ai_agent.id` — `resolveSessionForCharacter` in
`manager-api-node/src/services/character-resolver.js:11` sets
`characterId: character.id`, and `ai_agent.id` is a UUID string
(`prisma/schema.prisma:11`), so it survives JSON as a string. It is populated on
every dispatch path: hello (`virtual-connection.js:553`), the `hello.character_id`
wheel selection (`:585`, `:846`), the RFID card override (`:585`), and mode-change
(`mqtt-gateway.js:2229`, `:3461`).

**2. The worker only looks for `agent_id`, so it resolves to empty.**

[post_session_persistence.go:586](pkg/livekit/post_session_persistence.go#L586):

```go
keys := map[string]struct{}{"agent_id": {}, "agentid": {}}
return strings.TrimSpace(findFirstString(md, keys))
```

`character_id` is not in the set. `rs.agentID` is therefore `""` for every
Cheeko-platform session that has ever run.

**3. Empty `agentID` means the field is omitted from every persistence payload.**

- `/agent/chat-history/session` — [post_session_persistence.go:415](pkg/livekit/post_session_persistence.go#L415)
- `/agent/device/{mac}/sessions/{room}/summary` — [:358](pkg/livekit/post_session_persistence.go#L358)
- `/agent/chat-history/report` (manager-backed store) — [manager_api_backend.go:305](pkg/session/manager_api_backend.go#L305),
  fed from the same resolution via `workspace_lifecycle.go:24` → `manager_session_store.go:48`

**4. The manager fills the hole with the device's default agent.**

`agent.service.js:2270` (`batchUploadSession`) and `:2228` (`reportChatMessage`):

```js
let resolvedAgentId = agentId;
if (!resolvedAgentId) resolvedAgentId = await getAgentIdByMac(normalizedMac);
```

`getAgentIdByMac` (`:1412`) returns `ai_device.agent_id`. That value is then
written to both `voice_session_messages.agent_id` (`:2287`) and
`voice_sessions.agent_id` (`ensureVoiceSession`, `:468`).

**5. `ai_device.agent_id` deliberately does not change when the character does.**

`setCharacterByName` only persists when asked (`agent.service.js:1564`):

```js
// Session-scoped switches (e.g. RFID card taps) skip persisting the device's
// default agent — the character applies only to the dispatched session.
if (persist) { await prisma.ai_device.update({ ... agent_id: agent.id }); }
```

RFID taps and the firmware character wheel both call it with `persist: false`
(`virtual-connection.js:839`). So the default stays whatever the parent app set —
Cheeko — and every transcript inherits it.

**Cause: a key-name mismatch, not a lost value.** The correct id is in the room
metadata for every session; the worker just isn't reading that name, and both
sides silently paper over the gap (worker omits the field, manager guesses).

## What is wrong downstream today

Everything agent-scoped reads Cheeko's pile:

- `getAgentSessions` (`agent.service.js:317`), `getChatHistory` (`:374`),
  `getRecentUserChatHistory` (`:2331`) — all `where: { agent_id }`
- device bootstrap `recentMessages` (`:1841`) filters `agent_id: device.agent_id`,
  so the worker hydrates *and* the manager-backed store rehydrates from a pile
  that claims to be Cheeko's but is actually every character's
- `voice_sessions.agent_id` is equally wrong, and it is rewritten on every message
  upsert, so there is no surviving correct copy anywhere

## The fix

Add the gateway's spelling to the worker's key set —
[post_session_persistence.go:586](pkg/livekit/post_session_persistence.go#L586):

```go
// character_id is the gateway's spelling for the same ai_agent.id, and the only
// one dispatch metadata actually carries.
keys := map[string]struct{}{
    "agent_id": {}, "agentid": {}, "character_id": {}, "characterid": {},
}
```

Four lines. No manager, gateway, DB or config change.

`findFirstString` is right here (UUID → JSON string); `findFirstScalar` is not
needed, that exists for the numeric `kid_id`.

### Blast radius of a non-empty `agentID` — checked, all safe

Three other call sites read it, every one behind a branch that a real Cheeko
session never reaches (device MAC is always present):

| Site | Guard | Effect |
|---|---|---|
| `sessionKeyForParticipant` [room_session.go:1040](pkg/livekit/room_session.go#L1040) | only if `deviceMAC == ""` | none |
| `workspace_lifecycle.go:44` | only if `kidID == "" && deviceMAC == ""` | none |
| `livekitCronSessionKey` [main.go:1819](cmd/picoclaw-livekit/main.go#L1819) | only if `deviceMAC == ""` | none |

## The consequence someone must accept before this ships

Once history is attributed correctly, **agent-scoped reads stop returning other
characters' conversations** — which is the point, but it changes what screens show:

- The parent app's history for the device's default agent will show only Cheeko
  sessions, not the whole transcript pile. Sessions do not vanish (they carry
  `kid_id` and `mac_address`), but any screen keyed purely on `agent_id` shows less.
- Bootstrap `recentMessages` stops replaying Quizzy's turns into Cheeko's prompt.
  That is the cross-character contamination described in
  `docs/issues/greeting-latency/001-transcript-retention.md` and the reason
  Quizzy's and Riddler's prompts carry defensive "ignore the history" wording.

Resolved 2026-08-13: the read paths move to child × character scope in
`002-reads-scope-by-child-and-character.md`, which must ship with or immediately
after this one. Nothing here is deferred to "later" — it is the next ticket.

## Alternatives considered and rejected

- **Gateway also emits `agent_id`.** Same result, but two repos deploy instead of
  one, and the duplicate key is free to drift from `character_id`.
- **Manager stops falling back to `ai_device.agent_id`.** Correct in principle
  (guessing is what hid this for months), but on its own it converts wrong data
  into null data, and it breaks the legitimate callers that never send an id.
  Worth doing *after* this, as a hard error, so a future mismatch is loud.
- **`persist: true` on card taps.** Makes attribution right by making the device's
  default follow the card — reintroducing exactly the leak the `persist` flag was
  added to stop.

## Acceptance criteria

- [ ] `resolvePersistenceFields` returns the character id from metadata carrying
      only `character_id` — unit test alongside `TestResolvePersistenceFieldsFromMetadata`
      (`pkg/livekit/post_session_persistence_test.go:46`), plus one asserting
      `agent_id` still wins when both are present
- [ ] A live session with a non-default character writes that character's id to
      `voice_session_messages.agent_id` and `voice_sessions.agent_id`
- [ ] Two back-to-back sessions on one device with different characters produce
      two different `agent_id` values
- [ ] The summary `PUT` for that session carries the same `agentId`
- [ ] Backfill decision recorded: existing rows are unrecoverable per-message
      (the character is not stored anywhere else on the row), but
      `voice_sessions` can be partially reconstructed from
      `MEMORY.md`'s `[<character>]` summary labels — decide repair vs. accept
      and say which in the resolution
- [ ] Downstream read scoping decided (agent-scoped vs child-scoped) and either
      changed or explicitly deferred to its own ticket

## Verification

Before, on the dev DB — expect one dominant agent id:

```sql
SELECT a.agent_name, count(*)
FROM voice_session_messages m JOIN ai_agent a ON a.id = m.agent_id
WHERE m.created_at > now() - interval '7 days'
GROUP BY 1 ORDER BY 2 DESC;
```

After: tap the Quizzy card (or pick it on the wheel), talk, end the session, and
re-run scoped to that `session_id`. The worker log line
`Post-session chat history persisted` names the room; the row's `agent_id` must be
Quizzy's, not the device's default.

Note the trap from `greeting-latency/001`: a previous session's teardown flush can
land seconds into the next session. Read rows by `session_id`, never by "the most
recent N".
