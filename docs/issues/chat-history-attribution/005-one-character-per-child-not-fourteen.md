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

## Open question for whoever takes this

**Why is the app creating a new agent at all?** User 6's newest Cheeko was created
at 15:02, two minutes before a session on a device bound at 10:00 the same day.
Something in the bind/select flow POSTs an agent per action. Fixing only the
symptom leaves that loop running, so find the caller before closing.

Also unexplained: kid 15's 71 sessions belong to `83b7a273`, which is **not in
user 6's agent list at all** — that history is attributed to an agent under a
different account. Worth understanding before merging rows, since a naive merge
keyed on `user_id` will not gather it.

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
