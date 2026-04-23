# Conversation Recall And Rolling Memory Implementation Plan

Last updated: 2026-04-23

## Goal

Make Cheeko reliably answer questions like "what did we speak yesterday?" without stuffing all chat history into the prompt, and stop using `ai_agent.summary_memory` as a last-session-only memory field.

The target design separates memory into four layers:

- Recent context: the last small window of `voice_session_messages`, used for immediate continuity.
- Episodic history: per-session and per-day summaries, used for time-based recall.
- Stable memory: durable facts about the child/device, stored in `device_memory_documents`.
- Legacy mirror: `ai_agent.summary_memory`, kept only as a bounded compatibility summary, not the canonical memory source.

## Current Problem

Today, `voice_session_messages` contains the actual transcript and `voice_session_summaries` contains the latest session summary, but `ai_agent.summary_memory` is still being overwritten with a single session narrative.

That creates two failures:

- Asking about yesterday or last week does not naturally retrieve the correct historical sessions.
- The prompt can grow or become stale if we try to solve recall by loading too much history into startup context.

## Design Rules

- Never load all chat history into the system prompt.
- Resolve historical questions with retrieval, not prompt bloat.
- Filter by normalized MAC address first; optionally filter by `kid_id` and `agent_id`.
- Treat time expressions in the child's timezone, not server timezone.
- Use summaries first; fetch raw messages only when needed.
- Keep `ai_agent.summary_memory` as a small stable summary mirror for backward compatibility.
- Keep exact transcript recall grounded in `voice_session_messages`.

## Data Model Changes

Add `device_daily_conversation_summaries`.

Fields:

- `id uuid primary key`
- `mac_address varchar(20) not null`
- `device_id uuid null`
- `agent_id uuid null`
- `kid_id bigint null`
- `summary_date date not null`
- `timezone varchar(64) not null default 'Asia/Kolkata'`
- `summary text not null`
- `source_session_count int not null default 0`
- `source_message_count int not null default 0`
- `model varchar(100) null`
- `metadata jsonb not null default '{}'`
- `created_at timestamptz not null default now()`
- `updated_at timestamptz not null default now()`

Constraints and indexes:

- Unique: `(mac_address, summary_date)`
- Index: `(mac_address, summary_date desc)`
- Index: `(kid_id, summary_date desc)`
- Index: `(agent_id, summary_date desc)`

No immediate destructive changes to `ai_agent.summary_memory`.

## Manager API Contract

Add service-key protected recall endpoint:

`POST /toy/agent/device/:mac/conversation-recall`

Request:

```json
{
  "query": "what did we speak yesterday?",
  "timezone": "Asia/Kolkata",
  "now": "2026-04-23T10:00:00+05:30",
  "limit": 5,
  "includeMessages": false
}
```

Response:

```json
{
  "macAddress": "00:16:3e:ac:b5:38",
  "resolvedRange": {
    "label": "yesterday",
    "start": "2026-04-22T00:00:00+05:30",
    "end": "2026-04-23T00:00:00+05:30",
    "timezone": "Asia/Kolkata"
  },
  "answerContext": "Compact summary for the LLM to answer from.",
  "dailySummaries": [],
  "sessionSummaries": [],
  "messages": [],
  "citations": []
}
```

Supported query classes:

- `today`
- `yesterday`
- `last time` / `previous conversation`
- explicit dates
- recent broad history, such as "what did we talk about before?"

## PicoClaw LiveKit Contract

Add one of these integration paths:

- Preferred: expose a `recall_conversation` tool to the voice agent.
- Fallback: detect historical-recall intent before the LLM call and inject a compact recall result into the turn context.

The prompt rule should be:

When the child asks about previous conversations, yesterday, last time, earlier, or something remembered from past sessions, call conversation recall before answering.

The recall result must be added as a small turn-scoped context block, not written permanently into the system prompt.

## Implementation Strategy

Do this in two steps:

1. Short-term fix now: make `ai_agent.summary_memory` and `device_memory_documents(document_key='summary')` behave like rolling overall memory.
2. Long-term fix later: add explicit conversation recall for exact "last time", "yesterday", and date-based questions.

The short-term fix improves reconnect continuity quickly, but it must not become a substitute for exact historical recall.

## Phase 1: Short-Term Rolling Overall Memory

- [ ] Change `saveVoiceSessionSummary` so it no longer replaces `ai_agent.summary_memory` with the latest session summary.
  Verify: saving a new session summary updates `voice_session_summaries`, but `ai_agent.summary_memory` is not equal to the raw latest session summary.

- [ ] Add rolling overall-memory consolidation.
  Verify: old overall memory plus latest session summary produces one compact stable memory, capped to a fixed length.

- [ ] Mirror the rolling overall memory to `device_memory_documents(document_key='summary')` and `ai_agent.summary_memory`.
  Verify: both places contain stable facts/preferences, not a chronological last-session narrative.

- [ ] Add tests for memory stability.
  Verify: after two sessions, overall memory preserves durable facts and recent interests without losing the child's identity or becoming a transcript.

- [ ] Keep bootstrap context bounded.
  Verify: manager bootstrap still returns only stable memory plus the configured recent message window.

## Phase 2: Long-Term Conversation Recall

- [ ] Add Prisma model and migration for `device_daily_conversation_summaries`.
  Verify: `npx prisma validate`, `npx prisma generate`, and migration readiness tests pass.

- [ ] Add manager Prisma readiness guard for the new daily-summary delegate/table.
  Verify: startup guard fails clearly on missing table and passes after migration.

- [ ] Add daily summary rollup after session end.
  Verify: multiple sessions on the same local date upsert one `device_daily_conversation_summaries` row with increasing `source_session_count`.

- [ ] Add `conversation-recall` manager service and route.
  Verify: `yesterday` on `2026-04-23` with `Asia/Kolkata` returns only `2026-04-22` sessions for the requested MAC.

- [ ] Add recall tests for isolation.
  Verify: two devices using the same agent do not see each other's messages, summaries, artifacts, or daily rollups.

- [ ] Add PicoClaw recall integration.
  Verify: asking "what did we talk about yesterday?" triggers recall and the LLM receives only the compact retrieved context.

- [ ] Backfill daily summaries from existing `voice_session_summaries` and `voice_session_messages`.
  Verify: dry run reports counts, execution is idempotent, and rerunning does not duplicate rows.

- [ ] Add observability.
  Verify: logs include recall query, resolved date range, result counts, latency, and whether raw messages were included.

## Context Budget

Use these limits unless tests show a better number:

- Startup stable memory: max 1,500 characters.
- Recent bootstrap messages: max 20 messages by default.
- Recall answer context: max 2,500 characters.
- Daily summaries returned to LLM: max 3.
- Session summaries returned to LLM: max 5.
- Raw messages returned only when `includeMessages=true` or summaries are missing.

## Rollout Order

1. Stop overwriting `ai_agent.summary_memory` with latest-session summaries.
2. Add rolling overall-memory consolidation and mirror it to the device memory document.
3. Verify reconnect continuity with "do you remember me?" and broad memory questions.
4. Add schema and readiness guards for daily summaries.
5. Add daily rollup.
6. Add recall endpoint and unit tests.
7. Integrate PicoClaw recall tool/pre-retrieval.
8. Backfill existing history.
9. Enable in staging for one test device.
10. Verify with two-worker/load-balanced sessions.
11. Enable for all LiveKit devices.

## Done When

- [ ] On 2026-04-23, asking "what did we speak yesterday?" retrieves April 22 conversations in the child's timezone.
- [ ] `ai_agent.summary_memory` contains stable overall memory, not the latest session narrative.
- [ ] Cheeko can answer from previous sessions without loading all transcript history into the prompt.
- [ ] Prompt size stays bounded as total chat history grows.
- [ ] Two devices sharing the same agent never leak recall results across MAC addresses.
- [ ] Existing app chat-history APIs continue reading exact transcripts from `voice_session_messages`.

## Open Decisions

- Whether recall should be LLM-tool-driven or pre-retrieved by intent detection in `AgentBridge`.
- Whether daily summaries should be generated by deterministic concatenation first, then upgraded with an LLM summarizer.
- Whether semantic recall should use existing `device_memory_chunks`, pgvector, Qdrant, or Mem0 after the deterministic recall path is stable.
