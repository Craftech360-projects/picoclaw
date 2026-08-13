---
status: closed
assignee: claude
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

- [x] Creating a character that already exists for the account returns the
      existing agent; no second row
- [x] `"Cheeko 2"` still normalises to `Cheeko` and still resolves the template
      persona — the TTS bug does not come back
- [x] `/kids/:kidId/characters` shows each character exactly once, with session
      counts summed across duplicate rows — unit-proven; not yet called over HTTP
      with a Firebase token
- [x] Merge script repoints sessions, messages and devices **before** deleting,
      and a dry run reports what it would move
- [x] After merge, no `(user_id, agent_name)` has copies, and no history row has a
      NULL `agent_id` that had one before — nulls unchanged at 24 / 145
- [x] The caller creating spurious agents is identified and named here

## Found by

Observed in the admin console 2026-08-13 while verifying `004`.

## Extra acceptance criteria from those findings

- [x] Binding with a wrong or expired device code returns a 4xx, not a 500 —
      400/404/409 by case; not yet exercised from the app
- [ ] A failed bind leaves no orphan agent — **needs the app's own retry path**,
      which is the only place the create-then-bind loop runs
- [ ] The merge repoints by the session's `kid_id` owner, not by `ai_agent.user_id`;
      the 71 sessions on `00:16:3E:AC:B5:38` are the test case — **not done, and
      deliberately**: the read already presents them correctly, and moving them
      decides who owns a child's past
- [ ] Decided (either way, in writing) whether unbind clears `ai_device.agent_id`

## Progress 2026-08-13 — code shipped, merge is staged and waiting

Shipped in manager-api `73d1172e` + `f9c8a41` (script safeguards). **1503 tests
pass, 77 suites.** Deployed to the dev box.

| part | state |
|---|---|
| dedupe on create | done, 4 tests |
| read merges duplicates | done, 4 tests |
| bind returns 4xx not 500 | done |
| merge existing rows | **script written, dry run verified, NOT applied** |

The dry run against DB1:

```
duplicate clusters: 4, rows to remove: 22, references to move: 3299
cross-account history (reported, NOT changed):
  71 sessions  agent 83b7a273… (user 5) vs Kishore (user 6)
   9 sessions  agent 974f2f79… (user 3) vs Aditi   (user 4)
   2 sessions  agent 83b7a273… (user 5) vs Test Child (user 2)
   2 sessions  agent 56fb2ede… (user 1) vs Test Child (user 2)
```

Two things the dry run taught the script: Prisma's interactive transaction
defaults to a 5s timeout and one cluster moves ~1000 rows, so the move would abort
and leave the row in place; and deleting an agent is not reversible from the
database alone, so the loser→survivor mapping is now written to
`scripts/merge-duplicate-agents.audit.json` as it goes.

Note the read-side merge **already fixes Kishore's screen**, including the 71
cross-account sessions: the index groups by character name across every agent row
in the child's history, whatever account owns it. The merge is hygiene, not the
fix for the symptom.

## How to test this properly

### 1. Unit — done, re-run with `npx jest`

- `tests/unit/agent-character-dedupe.test.js` — existing name returns the existing
  row; `"Cheeko 2"` matches the `Cheeko` row (the normalised lookup, which is the
  bug); an unknown character is still created; the lookup is account-scoped
- `tests/unit/mobile.kid-chat-history.test.js` — three rows read as one character
  with counts summed and the newest row representing it; naming any duplicate
  returns all their sessions; a single-row character is unaffected

### 2. API on the dev box, before the merge

The point is that the read is correct **while duplicates still exist** — do this
before applying the script or the evidence is gone.

```sql
-- expect several rows named Cheeko for user 6
SELECT id, agent_name, created_at FROM ai_agent WHERE user_id = 6 ORDER BY created_at;
```

Then call `GET /api/mobile/kids/15/characters` as Kishore's parent (Firebase
token) and confirm **one** Cheeko entry whose `sessionCount` equals the sum across
all its rows — 73 at the time of writing — and that
`/kids/15/characters/:agentId/sessions` returns all of them for whichever
`agentId` the list gave back.

### 3. Create and bind, the loop that caused this

```
POST /api/mobile/agents {"agentName":"Cheeko"}     → returns the EXISTING id, twice
POST /api/mobile/agents {"agentName":"Cheeko 2"}   → same id again
POST /api/mobile/agents/:id/bind/000000            → 400, not 500
SELECT count(*) FROM ai_agent WHERE user_id = <u> AND agent_name = 'Cheeko';  -- unchanged
```

The row count before and after is the assertion; a failed bind must leave nothing
behind.

### 4. From the app itself

Activate a toy twice with a **wrong** code, then a right one, and confirm the
account gains **one** agent, not three. This is the original reproduction — the
15:01–15:02 log sequence — and it is the only test that exercises the app's own
retry path.

### 5. The merge

```bash
node scripts/merge-duplicate-agents.js            # dry run, read it
node scripts/merge-duplicate-agents.js --apply
node scripts/merge-duplicate-agents.js            # must now report no duplicates
```

Then verify nothing was stranded — this is the failure the ordering exists to
prevent:

```sql
-- must be 0: history that lost its character instead of moving
SELECT count(*) FROM voice_session_messages WHERE agent_id IS NULL;
SELECT count(*) FROM voice_sessions        WHERE agent_id IS NULL;
-- must be 0: an account still holding two rows for one character
SELECT user_id, lower(agent_name), count(*) FROM ai_agent
GROUP BY 1,2 HAVING count(*) > 1;
-- every device still names a character that exists
SELECT count(*) FROM ai_device d LEFT JOIN ai_agent a ON a.id = d.agent_id
WHERE d.agent_id IS NOT NULL AND a.id IS NULL;
```

Capture the session totals per child **before** the merge and compare after: the
totals must be identical. Rows move between agents; none may disappear.

### 6. Live, on a toy

One session per character after the merge, then confirm each still attributes
correctly and the device's default character still resolves — the merge repoints
`ai_device.agent_id`, so a toy whose row was a loser is the case to try.

## What is deliberately not done

The **cross-account history** is reported and left alone. Moving those 71 sessions
decides who owns a child's past, and the read already presents them correctly.
That needs its own decision, with `004`'s cutover note as precedent.

## Resolution — merge applied to DB1 2026-08-13

**22 agent rows removed, 3299 references moved, nothing stranded.**

| | before | after |
|---|---|---|
| `ai_agent` | 56 | **34** |
| `voice_sessions` | 656 | 656 |
| `voice_session_messages` | 3131 | 3131 |
| sessions with NULL agent | 24 | **24** |
| messages with NULL agent | 145 | **145** |
| accounts with duplicate rows | 4 | **0** |
| devices naming a missing agent | — | **0** |

The two NULL counts are the ones that matter: had the script deleted before
repointing, the `onDelete: SetNull` FKs would have converted moved history into
orphans, and those numbers would have jumped by 3299. They did not move. Per-child
session totals are identical across all eight children.

Kishore's history now reads `Cheeko 73, NANI 1, quizzy 1` where it read three
separate Cheekos. Note the 73 still spans **two** agent ids — user 6's survivor and
user 5's stray from the toy that changed hands — merged by the read, not by the
script. That is the intended split of responsibility.

Audit of every loser→survivor move is at
`manager-api-node/scripts/merge-duplicate-agents.audit.json` on the dev box.
Deleting an agent row cannot be undone from the database alone; that file is the
record.

**Not done, and each for a stated reason:** the app's own retry path (step 4 of
the test plan — needs the app), the live per-character run (step 6 — needs a toy),
moving cross-account history (a decision, not a cleanup), and whether unbind should
clear `ai_device.agent_id`.
