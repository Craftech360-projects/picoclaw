---
status: proposed
assignee: unassigned
---

# 000 — Chat history belongs to a child and a character

## What we want

1. Every conversation turn is attributed to **the child who spoke** and **the
   character they spoke to**.
2. The parent app can browse it: child → character → sessions → transcript.
3. A child moving to a new toy keeps their history. A sibling inheriting a toy
   sees none of it.
4. At runtime each character replays **only its own** raw turns, but all
   characters keep sharing the child's **session summaries**, so Cheeko still
   knows something happened with Quizzy without replaying Quizzy's turns.

Points 3 and 4 are decisions, taken 2026-08-13. Point 4 is the middle option:
split transcript, shared summaries.

## Where we are

`child-owned-state/006` already made the child half work: `voice_sessions.kid_id`
is populated on every upsert and `voiceSessionScope` (`agent.service.js:447`)
resolves reads by child with a null-guarded MAC fallback for unpaired devices.
The workspace directory is already per child (`kid-<id>`).

The character half is broken end to end, and one bug explains it: the worker
never sends a character id, so the manager stamps every message with
`ai_device.agent_id` — the device's default character. On these devices that is
Cheeko, which is why all six characters' history reads as Cheeko's. Full evidence
in `001`.

So the work is: fix the attribution (001), make the reads use it (002), split the
runtime transcript (003), then repair and verify what exists (004).

## The shape we are building toward

| Fact | Where it lives | Status |
|---|---|---|
| which child | `voice_sessions.kid_id` | done (006) |
| which character | `voice_sessions.agent_id`, `voice_session_messages.agent_id` | broken → 001 |
| which device | `voice_sessions.mac_address` | done, and **not** the identity anything reads by |
| per-session summary | `voice_session_summaries` | per session, so per character once 001 lands |
| raw turns replayed to the model | worker session key → `sessions/*.jsonl` | device-scoped → 003 |
| cross-character continuity | `memory/MEMORY.md` `## Session Summaries` | already child-scoped and `[character]`-labelled — **keep as is** |

Two invariants worth stating, because both have already been violated once:

- **A message reaches the child through its session**, never through a column of
  its own (`006`'s resolution). Do not add `kid_id` to `voice_session_messages`.
- **`ai_agent` is per parent account, not per child.** Two siblings share one
  Quizzy row. `agent_id` alone never separates children — the pair
  `(kid_id via session, agent_id)` does.

## Order and why

```
001  worker sends the real character id          ← nothing else is true without this
002  reads scope by (child × character)          ← makes the app view possible; also
                                                    fixes bootstrap reading the wrong
                                                    character once 001 lands
003  per-character transcript at runtime         ← independent of 001/002, but its
                                                    value is only visible after them
004  backfill + live verification                ← last, needs all three deployed
```

002 must ship **with or immediately after** 001. On its own, 001 makes device
bootstrap return the wrong character's recent messages (it filters
`agent_id: device.agent_id`, the default). Today that is invisible because every
row carries that same id.

## Out of scope

- Merging this into one chronological "everything the child said" feed. The app
  view is per character (decided). A merged timeline can be added later from the
  same rows.
- Per-child `ai_agent` rows. Characters stay account-level; children are separated
  by the session, which is cheaper and already works.
- `ai_agent_chat_history`, the legacy xiaozhi table — dead, removed in
  `child-owned-state/009`.

## Tickets

- `001-history-carries-the-speaking-character.md`
- `002-reads-scope-by-child-and-character.md`
- `003-per-character-transcript-shared-summaries.md`
- `004-backfill-and-live-verification.md`
