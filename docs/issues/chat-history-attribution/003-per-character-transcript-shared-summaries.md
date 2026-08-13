---
status: proposed
assignee: unassigned
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

- [ ] Two characters on one device produce two different session keys, and one
      character's turns never appear in the other's history — unit test on
      `sessionKeyForParticipant`, plus one on a real JSONL store asserting two
      files
- [ ] With `agentID` empty (metadata without a character) the key is byte-identical
      to today's, so nothing regresses on a dispatch that omits it
- [ ] The cron session key follows the same rule
- [ ] `MEMORY.md` still receives one labelled bullet per session and is still
      injected — assert the existing summary test still passes unchanged
- [ ] Live: talk to Cheeko, then tap Quizzy, and Quizzy's greeting prompt logs a
      `messages=` count that excludes the Cheeko turns while a summary is present
- [ ] Orphaned pre-change transcript file is removed, and the workspace upload
      does not carry it
- [ ] `saveMemory/{mac}` target confirmed and recorded in the resolution

## Blocked by

- `001-history-carries-the-speaking-character.md`

## Related

- `docs/issues/greeting-latency/001-transcript-retention.md` — the retention window
  and the contamination write-up this ticket closes
