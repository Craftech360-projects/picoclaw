---
status: open
assignee:
---

# 009 — Delete what the survey proved dead

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

Housekeeping that stops the next person auditing this area from having to re-derive
what the survey already established. Independent of every other slice — it can be
picked up first or last.

**Four tables are in `schema.prisma` but do not exist in DB1 at all:**
`device_memories`, `memory_chunks`, `user_question_quota`, `custom_content_pack`.
Remove the first three from the schema. `device_memories`/`memory_chunks` are the
pre-`device_memory_documents` design and carry the only pgvector `embedding` column
in the schema — worth one line in the removal commit noting that vector search over
memory was dropped in that migration and never rebuilt, so it is a decision rather
than an oversight.

**Keep `custom_content_pack`.** It is the child-keyed replacement for the
`CUSTOM_<MAC>` packs and is deliberately deferred, not dead — see the out-of-scope
section of the design.

**Eight tables exist but have zero rows and zero lifetime inserts:** every
`analytics_*` table, `game_session`, `kid_activity_log`, and `ai_agent_chat_history`.
Confirm no writer in any of the three repos, then remove them from the schema. Two
loose ends go with them:

- `getHomepageRecommendations` still reads `ai_agent_chat_history` as a boolean
  "has this child talked recently" signal. With no rows since 2026-06-06 that
  personalisation is permanently off. Repoint it at `voice_session_messages` or drop
  the branch — do not leave a read against a table that no longer exists.
- `agent.service.js` deletes `ai_agent_chat_history` rows on agent deletion. That
  goes too.

**Stop writing Mem0.** `MEM0_API_KEY` is unset on the dev box and the gateway already
returns empty memory arrays, so the integration is inert there — but the manager still
calls `addConversation` and `addFact` unconditionally when a key is present, keyed on
the **MAC**, which mixes siblings into one profile. Remove the write path rather than
re-keying it. Check the production environment before assuming the key is unset
there too.

Dropping the tables from the database itself is a separate, later decision — this
slice is about the schema and the code that references them.

## Acceptance criteria

- [ ] `device_memories`, `memory_chunks` and `user_question_quota` removed from
      `schema.prisma`; `custom_content_pack` retained
- [ ] The eight zero-row tables removed from `schema.prisma`, each confirmed to have
      no writer in manager-api, mqtt-gateway or picoclaw first
- [ ] `getHomepageRecommendations` no longer reads `ai_agent_chat_history`
- [ ] The agent-deletion path no longer references it either
- [ ] Mem0 writes removed from the manager; production env checked and the finding
      recorded in the commit message
- [ ] `npx prisma generate` succeeds and the manager boots — the boot guard lists
      required models, so a wrong removal fails loudly rather than at runtime
- [ ] Full backend suite green, with no assertion deleted to make it pass

## Blocked by

None — can start immediately.
