---
status: proposed
assignee: unassigned
---

# 005 — One Cheeko per account, not fourteen

## Parent

`docs/issues/chat-history-attribution/000-design.md`

## The symptom

The admin console lists **multiple identical `Cheeko` agents** for one account.
User 6 (Kishore's parent) has four. **User 3 has fourteen.**

It is not cosmetic: `GET /api/mobile/kids/:kidId/characters` from `002` groups by
`agentId`, so Kishore's screen shows **three separate "Cheeko" entries**, each
holding a different slice of his history:

```
Cheeko  83b7a273…  71 sessions
Cheeko  dcc6601f…   1 session
Cheeko  4e3a8c90…   1 session
NANI    bd083d4d…   1 session
quizzy  72f5d19b…   1 session
```

The feature this phase exists to deliver — "browse my child's conversations by
character" — is unusable while one character appears three times.

## Why

Two behaviours that are individually reasonable and together make duplicates:

1. **`createAgent` never dedupes** (`agent.service.js:152`, reached from
   `POST /api/mobile/agents` and the admin `POST /agent`). It creates
   unconditionally.
2. **The name is normalised on the way in.** The mobile app creates instances
   with a numeric suffix — `"Cheeko 2"`, `"Cheeko 3"` — and
   `normalizeCharacterName` (`character-resolver.js:3`) strips a trailing number:

   ```js
   const base = raw.replace(/\s+\d+$/, '').trim();
   ```

So four instances the app believed were distinct are stored under one display
name. The normalisation itself is correct and must stay — it was added because a
null-persona `"Cheeko 2"` agent got its own name **spoken aloud** as "Cheeko two"
/ "Cheeko do". The bug is that nothing dedupes after normalising.

`setCharacterByName` (`:1497`) already does the right thing — it looks the name up
case-insensitively for that user and only creates on a miss. `createAgent` is the
path that does not.

## What to build

**1. Dedupe on create.** `createAgent` resolves the normalised name against the
user's existing agents and returns the existing row instead of inserting a second.
This is the same lookup `setCharacterByName` already performs; the two should
agree.

**2. Make the read robust anyway.** `getKidCharacters` should present one entry
per character, not per agent row, merging session counts across duplicates. Even
with (1) fixed, the existing rows are out there, and a read that assumes
uniqueness will keep showing duplicates until every account is cleaned.

**3. Merge the existing rows.** For each `(user_id, agent_name)` with copies, pick
a survivor and repoint `voice_sessions.agent_id`, `voice_session_messages.agent_id`
and `ai_device.agent_id` at it, then delete the losers. Ordering matters: the FK
is `onDelete: SetNull`, so deleting first would silently orphan history to NULL
rather than moving it.

Sequence: (1) stops the bleeding, (2) makes the app correct immediately, (3)
cleans up. (2) can ship without (3).

## Both open questions, answered 2026-08-13

### Why the app creates an agent: one per toy activation, by design

`CheekoAI-Parent-App/lib/controllers/toy_activation_controller.dart:582-599`:

```dart
final existingAgents = await _agentService!.getUserAgents();
final agentName = _generateUniqueAgentName(existingAgents);   // "Cheeko", "Cheeko 2", …
newAgentId = await _agentService!.createAgent(agentName: agentName);
final bindResult = await _agentService!.bindDevice(agentId: newAgentId, deviceCode: _deviceCode);
```

The app models **a toy as an agent**, so every activation creates one. Two things
turn that into duplicates:

1. **The server erases the app's uniqueness scheme.** `_generateUniqueAgentName`
   (`:809`) produces `Cheeko 2`, `Cheeko 3`; `normalizeCharacterName` strips the
   suffix and stores them all as `Cheeko`. The app then reads them back, sees
   three agents all called `Cheeko` rather than the numbered names it wrote, and
   its counter generates `Cheeko 2` again. The two sides disagree about what an
   agent is called, and neither is wrong on its own.
2. **A failed bind leaves the agent behind.** The live sequence:

   ```
   15:01:40  POST /api/mobile/agents                200
   15:01:40  POST /api/mobile/agents/d09d1139…/bind/959539   500
   15:01:52  POST /api/mobile/agents                200
   15:01:52  POST /api/mobile/agents/ec1324f4…/bind/959539   500
   15:02:24  POST /api/mobile/agents                200
   15:02:24  POST /api/mobile/agents/0cce451a…/bind/949539   200
   ```

   A mistyped device code, retried twice. The controller comments say the agent is
   cleaned up in the catch block; two orphaned rows say otherwise. A bad code also
   returns **500** where it should be a 4xx, which is worth fixing on its own —
   the app branches on error type.

**This is why fix (1) is the right one.** Making `createAgent` idempotent per
`(user_id, normalised name)` neutralises both the retry loop and the app's broken
counter **without shipping an app release**. Sharing one `Cheeko` row across two
toys is correct under `000`: an agent is a character, a device is a toy, and
history separates by `kid_id` and session, not by agent row.

### The 71 sessions: a toy that changed accounts and kept its old default

| fact | value |
|---|---|
| agent `83b7a273` | `Cheeko`, **user 5**, created 2026-07-23 |
| Kishore, kid 15 | **user 6** |
| all 71 sessions | MAC `00:16:3E:AC:B5:38`, 2026-07-23 → 2026-08-07 |

That toy was activated under **user 5** — creating user 5's `Cheeko` as its
default — and later paired to a child of **user 6**. `ai_device.agent_id` was
never re-pointed, so every session recorded `kid_id` from the device's *current*
pairing and `agent_id` from its *stale* default. One row, two accounts.

It is the same "the device default is a stale pointer" family that `001` fixed for
characters, and it has a direct consequence for step (3):

> **Merge by the child, not by the agent's account.** For each session, the
> correct survivor is the canonical agent of that name belonging to **the account
> that owns the session's `kid_id`**. A merge keyed on `ai_agent.user_id` leaves
> these 71 pointing at user 5 forever, and a careless one would move user 5's own
> history.

Related and worth its own decision: unbinding a toy should clear or re-point
`ai_device.agent_id`, or the next family inherits the previous owner's character
row.

## Acceptance criteria

- [ ] Creating a character that already exists for the account returns the
      existing agent; no second row
- [ ] `"Cheeko 2"` still normalises to `Cheeko` and still resolves the template
      persona — the TTS bug does not come back
- [ ] `/kids/:kidId/characters` shows each character exactly once, with session
      counts summed across duplicate rows
- [ ] Merge script repoints sessions, messages and devices **before** deleting,
      and a dry run reports what it would move
- [ ] After merge, no `(user_id, agent_name)` has copies, and no history row has a
      NULL `agent_id` that had one before
- [ ] The caller creating spurious agents is identified and named here

## Found by

Observed in the admin console 2026-08-13 while verifying `004`.

## Extra acceptance criteria from those findings

- [ ] Binding with a wrong or expired device code returns a 4xx, not a 500
- [ ] A failed bind leaves no orphan agent — verified by retrying a bad code twice
      and counting rows before and after
- [ ] The merge repoints by the session's `kid_id` owner, not by `ai_agent.user_id`;
      the 71 sessions on `00:16:3E:AC:B5:38` are the test case
- [ ] Decided (either way, in writing) whether unbind clears `ai_device.agent_id`
