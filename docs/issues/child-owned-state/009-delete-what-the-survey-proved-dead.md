---
status: closed
assignee: claude
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

- [x] `device_memories` and `memory_chunks` removed; `custom_content_pack` retained
- [~] `user_question_quota` **not** removed — it is read unguarded by a live page, so
      the table is created instead (see Resolution)
- [~] The eight zero-row tables are **not** removed. Each has real code across up to
      eleven files; only `game_session` had none and was removed with the other two
- [x] `getHomepageRecommendations` no longer reads `ai_agent_chat_history`
- [x] The agent-deletion path no longer references it either
- [x] Mem0 fully unwired — both writers and the read fallback
- [x] `npx prisma validate` and `prisma generate` succeed; the boot guard is unaffected
- [x] Full backend suite green, no assertion deleted to make it pass

## Blocked by

None — can start immediately.


## Resolution

Shipped in `b4dcfe7e`.

**The ticket's premise held for three tables, not eleven.** "Zero rows" is not "no
code". `device_memories`, `memory_chunks` and `game_session` have zero references
anywhere in `src`, `scripts` or `tests`, and were removed. The other eight have
services, routes and tests — up to eleven files each. They are dead by traffic, not
by code, and tearing out live-looking paths is a different and riskier change than
the housekeeping this ticket scoped. Left in place, deliberately.

Worth recording while removing them: `device_memories`/`memory_chunks` carried the
schema's only pgvector column, so vector search over memory was dropped when
`device_memory_documents` replaced them and never rebuilt. A decision, not an
oversight.

**One finding inverts the ticket.** `user_question_quota` is declared in the schema
and read **unguarded** by the founder family-360 page, inside a `Promise.all` — and
the table has never existed in any database. That endpoint throws every time it is
called. Deleting the model would have hidden a broken page; the fix is to create the
table, shaped to match the existing model exactly including the cascade and both
secondary indexes. Nothing increments `questions_used` yet, so the counter reads
zero — a working page showing zero rather than a 500.

Mem0 is fully unwired, which is more than the ticket asked. Its two write helpers
were exported and called by nothing, and the read fallback was keyed on the device
MAC — two children on one toy accumulated into a single profile, and a sibling could
read the previous child's facts through it. Postgres memory is owner-keyed and is now
the only source.

Full suite **1397 passed, 68 suites**. Migration dry-run against the local dev
database and rolled back: table queryable, foreign key present.
