---
status: closed
assignee: claude
---

# 003 — Per-character transcript, shared summaries

## Parent

`docs/issues/chat-history-attribution/000-design.md`

## The problem

The worker keys session state on the device alone —
[room_session.go:1033](../../../pkg/livekit/room_session.go#L1033):

```go
return "livekit:device:" + strings.ReplaceAll(rs.deviceMAC, ":", "")
```

One key means one `sessions/livekit_device_<mac>.jsonl` shared by every
character. A child who plays with Cheeko and then taps Quizzy has Cheeko's turns
replayed into Quizzy's prompt. This is documented, measured, and already being
fought in the prompts: Quizzy's and Riddler's `greeting_prompt` both carry
defensive wording telling the model to ignore what it sees in the history
(`docs/issues/greeting-latency/001-transcript-retention.md`, "Cross-character
contamination").

`001` and `002` fix what is *stored* and *read back*. They do not touch this,
because on the default deployment (`session_store_enabled=false`) the JSONL file
**is** the history the model replays.

## What to build

**Split the raw transcript, keep the summaries shared.** That is the decision:
characters should not replay each other's turns, but should still know the child
had a quiz this morning.

### A. The session key carries the character

Append the character to the key. The workspace directory is already per child
(`workspace-kid-<id>`), so the key does not need the child in it — the directory
supplies that, and adding it twice would be a second copy of a fact free to drift:

```go
// per (child × character): the directory is the child, the suffix is the character
key := "livekit:device:" + macNoColons
if rs.agentID != "" { key += ":agent:" + sanitizeIdentity(rs.agentID) }
```

`rs.agentID` is the character's `ai_agent.id` **only after `001`** — this ticket
is worthless before it, since every character would still hash to the same
suffix.

Match the same rule in `livekitCronSessionKey`
([main.go:1815](../../../cmd/picoclaw-livekit/main.go#L1815)), or a scheduled task
writes to a different file than the voice session it belongs to.

### B. Summaries stay where they are — do not "fix" them

`memory/MEMORY.md` `## Session Summaries` is child-scoped and already labels each
bullet with the character that produced it (`persistSummaryToMemoryFile`,
[post_session_persistence.go:311](../../../pkg/livekit/post_session_persistence.go#L311)).
It is injected into the dynamic per-request context, newest 10
(`promptSessionSummaryCap`). **No change.** That file is the shared-continuity
half of the decision; scoping it per character would delete the thing we chose to
keep.

### C. Two follow-on effects to handle, not discover later

1. **Existing transcripts orphan.** The old `livekit_device_<mac>.jsonl` stops
   being read the moment the key changes; new per-character files start empty.
   Each character therefore has one session with no replayed turns — softened by
   the shared summaries, which survive. Delete the orphan on first hydration
   rather than leaving it to inflate every workspace sync.
2. **Retention becomes per character**, since `ExpireStaleTranscript` works per
   key. A character not played with for 30 minutes resets independently. That is
   an improvement, but the 30-minute window and the 45s reconnect hint were tuned
   against one shared file — re-read `greeting-latency/001` before changing either.

### D. Check before assuming (do not skip)

`SetSummary` on the manager-backed path writes
`PUT /agent/saveMemory/{mac}` with `{summaryMemory}`
([manager_api_backend.go:312](../../../pkg/session/manager_api_backend.go#L312)).
Confirm which row that lands on. If it writes the *device's default* agent's
`summary_memory`, it is the same class of bug as `001` and belongs in this ticket;
if it is device-scoped, it is the shared-summary behaviour we want. This path is
inactive by default (`session_store_enabled=false`), so it is a correctness item,
not a blocker.

## Acceptance criteria

- [x] Two characters on one device produce two different session keys, and one
      character's turns never appear in the other's history — unit test on
      `sessionKeyForParticipant`, plus one on a real JSONL store asserting two
      files
- [x] With `agentID` empty (metadata without a character) the key is byte-identical
      to today's, so nothing regresses on a dispatch that omits it
- [x] The cron session key follows the same rule
- [x] `MEMORY.md` still receives one labelled bullet per session and is still
      injected — assert the existing summary test still passes unchanged
      (there was no existing test; `TestSessionSummariesStayInOneSharedMemoryFile`
      was added instead, and `persistSummaryToMemoryFile` is untouched)
- [ ] Live: talk to Cheeko, then tap Quizzy, and Quizzy's greeting prompt logs a
      `messages=` count that excludes the Cheeko turns while a summary is present
      — **no device in this session; moves to 004**
- [x] Orphaned pre-change transcript file is removed — asserted at the unit
      boundary. That the workspace *upload* then omits it is unverified: the
      sync reads the directory, so it follows by construction, but no live sync
      was run — **004**
- [x] `saveMemory/{mac}` target confirmed and recorded in the resolution

## Blocked by

- `001-history-carries-the-speaking-character.md`

## Related

- `docs/issues/greeting-latency/001-transcript-retention.md` — the retention window
  and the contamination write-up this ticket closes

## Resolution

Shipped in `94be8eb`. Section A and C.1 as written; section B untouched, as
instructed. Section D's premise turned out to be stale — see below.

### What shipped

**The key carries the character.** `sessionKeyForParticipant`
(`room_session.go`) appends `:agent:<sanitized id>` when `rs.agentID` is
non-empty, and `livekitCronSessionKey` (`main.go`) does the same with
`routing.NormalizeAgentID`. The two transforms differ in name but agree on an
`ai_agent` UUID, which is the only thing `character_id` ever carries; the two
tests assert the same literal so a drift fails rather than silently splits the
cron task off from its session.

With `agentID` empty both keys are byte-identical to the old ones, asserted
directly (`TestSessionKeyForParticipantWithoutAgentIDIsUnchanged`,
`TestLivekitCronSessionKeyWithoutAgentIDIsUnchanged`). The `deviceMAC == ""`
fallbacks are untouched.

**The orphan is deleted, not truncated.** `discardLegacyTranscript` runs in
`handleTrackSubscribed` immediately after `ExpireStaleTranscript` — the same
"before anything reads the history" point — and removes both
`livekit_device_<mac>.jsonl` and its `.meta.json` when the live key differs from
the device-wide one. It is a no-op when `agentID` is empty (the keys are equal,
so there is nothing orphaned and the live file must survive), and a no-op on the
manager-backed store, which has no local file to retire.

Deletion needed a new capability, added the way `LastActivity` already was:
`JSONLStore.Delete` (not on the `Store` interface, so `ephemeralSessionStore`
and the manager backend are untouched), `JSONLBackend.Delete` reaching it by
type assertion, and a `session.SessionDeleter` optional interface for the
livekit side to assert on. Four thin methods; no store is forced to grow one.

### Section D: `saveMemory/{mac}` is device-scoped, and the ticket's fear is stale

`PUT /agent/saveMemory/{mac}` → `agentService.saveMemory`
(`agent.service.js:2080`) → `saveRollingOverallMemory` (`:755`) →
`saveDeviceMemoryDocument` (`:1135`). That writes
`device_memory_documents` upserted on `(owner_key, document_key='summary')`,
where `owner_key` is `ownerKeyForDevice(device)` — the child when the device is
paired, the MAC when it is not. The read side (`getExistingOverallMemory`,
`:741`) resolves by the same owner key.

So it is **owner-scoped, i.e. the shared-summary behaviour this ticket wants**.
It is not the `001` class of bug, and there is nothing to fix here.

Two details worth recording because they read like a bug and are not:

- `saveMemory` looks up `ai_device.agent_id` and passes it as `agentId`.
  `saveRollingOverallMemory` does not destructure that parameter — it is
  ignored. The lookup survives only as an existence guard (`throw 'Device or
  agent not found'`).
- The stored row does carry an `agent_id` column, set to `device.agent_id`, the
  default character. Nothing keys off it; it is descriptive, not identifying. If
  anything ever starts filtering on it, it will be wrong for the same reason
  `001` was.
- `ai_agent.summary_memory` is explicitly no longer written (comment at
  `agent.service.js:772`) — `child-owned-state` already moved this off the
  account-level character row. The ticket was written against the older shape.

### What is not verified

Everything live. No device was available, so the greeting `messages=` count, the
workspace upload omitting the orphan, and the per-character retention behaviour
in C.2 are all proven only at the unit boundary. They belong to `004`.

The 30-minute retention window and the 45s reconnect hint were **not touched**.
C.2 is now true as a consequence: `ExpireStaleTranscript` works per key, so each
character ages out independently. Nothing about the window's tuning changes,
but it does mean a character played with once a week now always opens clean,
where previously another character's activity could keep the shared file warm.
That is the intended direction; it is recorded here so it is not read later as a
regression.

### One pre-existing failure, not from this change

`TestSynthesizeAndPlayLogsTTSProviderType` in `pkg/livekit` fails on this branch
with or without these changes — confirmed by running the package on the clean
tree before writing any code. Untouched, unrelated.

`make test` reports `pkg/livekit` and `cmd/picoclaw-livekit` as `[setup failed]`
(it runs `CGO_ENABLED=0`) and a set of Windows-environment failures in
`pkg/agent`, `pkg/auth`, `pkg/config`, `pkg/migrate`, `pkg/providers`,
`pkg/skills`, `pkg/tools` and `pkg/voice` — all identical with these changes
reverted.
