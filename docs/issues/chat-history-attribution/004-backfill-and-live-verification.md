---
status: in-progress
assignee: claude
---

# 004 — Backfill and live verification

## Parent

`docs/issues/chat-history-attribution/000-design.md`

## What this covers

001–003 make new conversations correct. This ticket decides what happens to the
existing pile and proves the whole thing works from a real toy.

## The existing rows

Every `voice_session_messages` row written to date carries the device's default
character. **Per-message repair is impossible** — the row holds
`(session_id, mac_address, agent_id, sequence, role, content, created_at)` and
nothing else that names who spoke. The character is simply not recorded anywhere
on it.

Per *session* there is one partial source: `memory/MEMORY.md` in each workspace
carries `- <timestamp> [<character>] (N messages): …` bullets, written by
`persistSummaryToMemoryFile`. Those only exist for sessions that produced a
summary, and only since the labelling was added — earlier bullets are unlabelled.

So the realistic options are:

**A. Accept it (recommended).** Leave history before the deploy attributed to the
device default; correct attribution starts at the cutover. Record the cutover
timestamp so the app can caption older sessions honestly rather than showing them
under a character that may not have spoken them.

**B. Partial session repair.** Match `MEMORY.md` bullets to `voice_sessions` by
timestamp and message count, and correct `voice_sessions.agent_id` where a
confident match exists. Messages stay wrong, so a session would list under one
character while its rows claim another — worse than A unless the message rows are
updated from their session, which is a guess dressed as data.

**C. Delete pre-cutover history.** Clean, defensible on retention grounds, and
throws away real conversations parents may already have seen.

Pick one and write it in the resolution. Do not leave this implicit.

## Live verification

Run after all three are deployed to the dev box (never prod — see
`cheeko-deploy-boundaries`). One device, one child paired.

1. Talk to the default character. End the session.
2. Tap a card for a second character. Talk. End.
3. Tap a third. Talk. End.

Then check, per `session_id` and never "the most recent N" — a previous session's
teardown flush lands seconds into the next one
(`greeting-latency/001`, "Why the A/B could not be run back-to-back"):

```sql
SELECT s.session_id, a.agent_name, s.kid_id, count(m.*) AS messages
FROM voice_sessions s
JOIN ai_agent a ON a.id = s.agent_id
LEFT JOIN voice_session_messages m ON m.session_id = s.session_id
WHERE s.started_at > now() - interval '1 hour'
GROUP BY 1,2,3 ORDER BY min(s.started_at);
```

Expect three rows, three distinct `agent_name`s, one `kid_id`, and
`voice_session_messages.agent_id` matching its session on every row.

Then, in the worker workspace: three `sessions/*.jsonl` files, one per character,
and one `memory/MEMORY.md` carrying three labelled bullets.

## The toy-swap test — the reason for the whole plan

1. Note the child's `kid_id` and the sessions above.
2. Pair the same child to a second device (different MAC).
3. Hold one conversation on the new toy.
4. `GET /api/mobile/kids/:kidId/characters` and the per-character session list.

**Pass:** the list spans both MAC addresses; nothing is missing; the transcript of
an old session still opens.

Then the inverse, which is the one that actually protects a family:

5. Pair a *different* child to the first device.
6. Read that child's history.

**Pass:** empty. Not "empty-ish" — the previous child's sessions must not appear
under any character, and the new child's workspace must not restore the previous
child's transcript.

## Acceptance criteria

- [x] Backfill option chosen, executed or explicitly declined, and recorded —
      **option A**, decided 2026-08-13
- [x] Cutover timestamp recorded somewhere the app can read or the docs can cite —
      **2026-08-13 13:46 IST, dev box only** (see below)
- [x] ~~Three~~ **One**-character live run passes the SQL check above — a real
      Nani session on 2026-08-13 filed as **Nani**, session and messages both.
      Two more characters still wanted for the full check
- [ ] Per-character `sessions/*.jsonl` files confirmed in the workspace — same
      blocker; the dev box currently holds one workspace and zero transcripts
- [x] Toy-swap **inverse** direction (new child sees nothing of the previous one) —
      `00:16:3E:7A:11:C4` has carried **two** children, kid 15 (2 sessions) and
      kid 16 (1), and the sessions separate cleanly by `kid_id` with no overlap.
      Asserted at the data boundary the endpoint filters on; the endpoint itself
      is Firebase-authed and was not called
- [ ] Toy-swap **forward** direction (same child, second toy) — still needs one
      child paired to two devices
- [ ] Parent-app screens rendered against real data, not fixtures — needs the app,
      which has not been built against the new endpoints yet
- [x] Result written into the resolution of each of 001–003, including anything
      that did not behave as the ticket predicted — one defect found, see below

## Blocked by

- `001-history-carries-the-speaking-character.md`
- `002-reads-scope-by-child-and-character.md`
- `003-per-character-transcript-shared-summaries.md`

## Progress 2026-08-13 — deployed to dev, one defect found, live run still owed

### Backfill: option A, and the baseline that justified it

Measured on DB1 before deploying:

```
BEFORE (30 days, by character): [{"agent_name":"Cheeko","messages":1635}]
sessions: 650 total, 306 with kid_id, 626 with agent_id
```

**1635 messages, one character, zero others.** Six characters are in daily use —
the gateway log shows `tara`, `masti`, `quizzy` dispatched on the test device the
same afternoon — and not one of them appears. Every device row's `agent_id` points
at a `Cheeko` row. Nothing to repair per message: option A stands, cutover is
**2026-08-13 13:46 IST on the dev box**. Production is untouched, so prod history
has no cutover yet.

### A defect the unit tests could not have found

`character_id` is **two different namespaces sharing one key name**:

```
[MQTT-IN][DEVICE] payload={"type":"hello",...,"character_id":"tara",...}
🎭 [CHARACTER] hello.character_id="tara" → set-character (session-scoped)
```

The firmware sends a **slug**; room metadata sends the `ai_agent` **UUID**. Both
are called `character_id`, and `findFirstString` recurses into nested objects, so
the two can meet. `voice_session_messages.agent_id` is `@db.Uuid` with an FK — a
slug reaching it fails the insert and loses the **entire transcript**, which is
strictly worse than the mis-attribution 001 exists to fix.

Fixed in `12cb296`: `character_id` is only believed when it matches a UUID;
anything else falls back to empty, which is the old, safe behaviour. `agent_id`
keeps its permissive handling — nothing has ever sent it, and other picoclaw
deployments may not use UUIDs. Two tests cover it, including the three real slugs
seen in the logs.

Both the code review and I had reasoned this was safe from the source alone. It
was reading the *logs* that showed the slug in live traffic.

### What is proven, and how

Deployed: picoclaw `12cb296` built on the box with cgo, `manager-api` `637e936c`,
both restarted and healthy, worker re-registered as `cheeko-agent`. No Prisma
migration in either pull (checked before restarting, since `server.js` applies
every unapplied migration on boot).

The persistence contract was then driven directly against
`POST /toy/agent/chat-history/session` — the exact endpoint the worker calls —
on real devices, and the rows read back:

| Case | Sent | `voice_sessions` | message rows | kid |
|---|---|---|---|---|
| A | `agentId` = riddler | riddler | riddler ×2 | – |
| B | `agentId` omitted | **Cheeko** | **Cheeko** | – |
| C | omitted, paired device | Cheeko | Cheeko | **3** |

A proves a named character is honoured end to end. **B reproduces the bug's exact
mechanism on live infrastructure** — omit the field and the Manager silently
substitutes the device default. C confirms `kid_id` still lands, which is what
makes a toy swap survivable. All three test sessions were deleted afterwards
(`CLEANED_UP=3`, `MESSAGES_LEFT=0` — the cascade works).

### What is still owed, and why an agent cannot do it

The three-character conversation, the per-character `.jsonl` files, and the
toy-swap test all need a child speaking into a toy. `client.py` takes audio from a
live microphone and has no file-input flag, so no agent can drive it. The runbook
below is ready for whoever has the device.

### 2026-08-13, later — the fix confirmed on a real toy

A live **Nani** session on `00:16:3E:AC:B5:38`, driven from the actual device:

```
SESSION {"session_id":"b6b23edb…_00163EACB538_conversation",
         "session_character":"Nani","msg_characters":"Nani","msgs":2}
SESSION {"session_id":"64d19efe…_00163EACB538_conversation",
         "session_character":"Nani","msg_characters":"Nani","msgs":1}
```

Both the session row and its message rows say **Nani**. Before today every one of
these would have read Cheeko — the 1635-message baseline above is what that looks
like. The worker log shows it carrying the character through:
`character_id=7a7a10d8-6893-42ce-b41c-6439776672a5&character=Nani`, a UUID, so the
guard in `12cb296` passes it rather than rejecting it.

`kid` is null on these rows because **the test device is unpaired**
(`kid_id: null`). That is correct behaviour, not a defect — but it means the
toy-swap criteria still need a paired device.

Two unrelated things surfaced in the same run:

- **`Speculative quiz batch fetch did not complete … context canceled`** — benign.
  A prefetch cancelled by the session ending; no action.
- **`device_memory_documents` upsert fails on a legacy unique constraint**, so the
  child's rolling memory can no longer update on any re-paired device (89 such
  rows on DB1). **Pre-existing**, inherited from `child-owned-state`, and verified
  not to come from this phase — 002's whole change to `agent.service.js` is one
  `const agentId` plus three substitutions in the bootstrap reads, none of them
  near that path. Written up as
  `docs/issues/child-owned-state/011-memory-doc-keeps-a-legacy-unique.md`. It
  matters here because it silently disables the shared-summary continuity that
  `000`'s point 4 depends on.

### 2026-08-13, second device — attribution beats the device default

`00:16:3E:7A:11:C4`, paired to kid 15, **device default character `Cheeko`**:

```
15:05  session_character=NANI    kid=15  msgs=5   ← after the deploy
11:05  session_character=Cheeko  kid=16  msgs=3   ← before the deploy
```

This is the decisive one. The 15:05 session is filed as **NANI while the device's
`ai_device.agent_id` still points at Cheeko** — the exact substitution that
produced the 1635-message all-Cheeko baseline, now not happening. Nothing before
today could have produced that row.

Two further facts from the same device:

- Its `summary` document has `owner_key: kid:15`, **updated 15:06** — one minute
  after the session. So the `011` constraint failure is not universal: a device
  whose owner key still matches its pairing consolidates fine. That is the
  positive control for `011`'s diagnosis, and it narrows the blast radius to
  re-paired devices only.
- The toy has carried **two children** (kid 15 and kid 16) with cleanly disjoint
  sessions, which is the inverse toy-swap case holding at the data boundary.

One operational gotcha worth keeping: the dev box's `SERVICE_SECRET_KEY` line
carries quoting/whitespace that makes an invalid HTTP header value. Node rejects
the request at the parse layer with a bodiless `400 Bad Request` that looks
nothing like a validation error. Strip it: `tr -d "\"'" | tr -d '[:space:]'`.
