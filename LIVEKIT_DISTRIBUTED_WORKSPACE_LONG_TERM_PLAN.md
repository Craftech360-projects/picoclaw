# PicoClaw LiveKit Distributed Workspace Long-Term Plan

Date: 2026-04-22

## Executive Decision

The LiveKit voice worker must be treated as stateless compute. A device should be able to connect to any worker instance and receive the same identity, session history, summaries, memory, and agent configuration. The local `workspace-device-<mac>` directory can remain as a short-lived runtime cache, but it must not be the source of truth.

The durable source of truth should be the manager API plus Postgres, but the primary per-call startup context should come from LiveKit room metadata created by `D:\cheeko-backend\main\mqtt-gateway`. On every new LiveKit room/job, the MQTT Gateway should enrich the dispatch metadata with device identity, child profile, character/session config, and memory context. The worker should create an ephemeral local workspace from that metadata, stream durable session writes back through the manager API, and persist final summaries/memory at close. This removes the need for sticky routing and prevents history loss when the second connection lands on another instance.

## What Exists Today

### PicoClaw LiveKit Worker

Current code creates a workspace identity from the LiveKit room/device:

- `cmd/picoclaw-livekit/main.go` resolves `deviceMAC` and `persistentAgentID`.
- If a MAC exists, workspace identity becomes `device-<mac-without-colons>`.
- It writes a rendered `IDENTITY.md` into `workspace-device-<mac>`.
- `preserveWorkspace = true` for device and persistent agent identities.
- `pkg/livekit/agent_bridge.go` deletes workspaces only when `preserveWorkspace` is false.

That means a single instance can preserve a device workspace across calls. In a multi-instance deployment, another worker will not have that local directory, so the user can appear new unless state is hydrated from a shared backend.

The worker already does some post-session persistence:

- `pkg/livekit/post_session_persistence.go` posts usage to `/toy/device/token-usage`.
- It posts chat history to `/toy/agent/chat-history/session`.
- Chat persistence is end-of-session batch upload, not turn-by-turn durable persistence.

PicoClaw also has swappable session storage:

- `pkg/session/session_store.go` defines the session persistence interface.
- `pkg/session/jsonl_backend.go` stores session state through the JSONL memory store.
- `pkg/agent/memory.go` stores long-term memory in local workspace files like `memory/MEMORY.md` and daily notes.

This is a good extension point. A manager-backed implementation can be added without rewriting the agent loop.

### Manager API

Manager API path inspected:

`D:\cheeko-backend\main\manager-api-node`

It is a Node/Express service using Prisma, `pg`, Supabase clients, Firebase auth, Qdrant client, and Mem0 integration.

Relevant mounted routes are under `/toy`:

- `/toy/agent` for agent prompt/config, character switching, chat history, memory, and LiveKit-facing endpoints.
- `/toy/device` for device registration, mode, token usage, and device lookup.
- `/toy/usage` for authenticated token usage analytics.

Important existing LiveKit-related endpoints:

- `GET /toy/agent/prompt/:mac`
- `GET /toy/agent/config/:mac`
- `GET /toy/agent/agent-id/:mac`
- `GET /toy/agent/device/:mac/current-character`
- `PUT /toy/agent/saveMemory/:mac`
- `POST /toy/agent/chat-history/report`
- `POST /toy/agent/chat-history/session`
- `POST /toy/device/token-usage`
- `GET /toy/usage/tokens/:macAddress/session/:sessionId`

Important existing service behavior:

- `agent.service.js` resolves `ai_device.mac_address` to `ai_device.agent_id`, then fetches `ai_agent`.
- `saveMemory(mac, summaryMemory)` writes to `ai_agent.summary_memory`.
- `reportChatMessage` writes one message into `ai_agent_chat_history`.
- `batchUploadSession` writes a batch into `ai_agent_chat_history`.
- `getPromptWithMemories` can call Mem0 by normalized MAC if Mem0 is configured.
- `device.service.js` aggregates token usage by `(mac_address, usage_date)`.

### Deployed Database

Read-only schema inspection confirmed:

- Database: Postgres 17.6, schema `public`.
- Table count: 55.
- Extensions include `pgcrypto`, `uuid-ossp`, Supabase extensions, but not `vector`.
- Existing key tables:
  - `ai_device`
  - `ai_agent`
  - `ai_agent_chat_history`
  - `device_token_usage`
  - `kid_profile`
  - `parent_profile`
- Missing from deployed DB even though they appear in Prisma schema:
  - `device_memories`
  - `memory_chunks`
- Row estimates for the inspected key runtime tables were currently `0`.

Important deployed constraints/indexes:

- `ai_device.mac_address` is unique.
- `ai_agent_chat_history` has indexes on `agent_id` and `session_id`, but no deployed index on `mac_address`.
- `device_token_usage` has a unique key on `(mac_address, usage_date)`.

## The Core Risk

Yes, the concern is real.

With the current local workspace design, if device A first connects to worker instance 1, that instance creates or reuses `workspace-device-A`. If the next call lands on worker instance 2, worker 2 will not have instance 1's local workspace. Unless the manager API/database is used to hydrate memory and history, worker 2 behaves closer to a first-time connection.

Some data is already sent to the manager API after a call, but this is not enough for strong continuity:

- End-of-session upload can lose the whole call if the worker crashes before close.
- Local `memory/MEMORY.md`, daily notes, and JSONL session state are not durably shared.
- `ai_agent.summary_memory` is agent-scoped, not clearly device/kid-scoped. If multiple devices share an agent, this can leak or overwrite memory.
- `device_token_usage` is a daily aggregate table, so it cannot reliably answer per-session usage after multiple sessions on the same day.
- Prisma schema and deployed DB are out of sync for the proposed memory tables.

## Target Architecture

### Principle

Workers are disposable. Device state is durable.

Every LiveKit worker should follow this lifecycle:

1. Resolve stable identifiers from room metadata: `mac_address`, `agent_id`, `kid_id`, `user_id`, `session_id`.
2. Validate the MQTT Gateway room metadata payload as the primary bootstrap context.
3. Call manager API bootstrap only if room metadata is missing, malformed, stale, or incomplete.
4. Render local runtime files like `IDENTITY.md` from the selected bootstrap context.
5. Run the conversation using a manager-backed session store.
6. Persist messages, summaries, usage, and memory incrementally during the session.
7. Treat local workspace files as rebuildable cache.
8. Delete or recycle the local workspace after the call once writes are flushed.

### Recommended Source Of Truth

Use Postgres behind the manager API for canonical state:

- Device identity and ownership: `ai_device`, `kid_profile`, `parent_profile`.
- Agent configuration: `ai_agent`.
- Session messages: new append-only session tables, with existing chat-history APIs reading from those tables.
- Session summary: new device/session-scoped summary table.
- Long-term memory: Postgres memory tables plus optional Mem0/Qdrant integration.
- Usage: separate per-session usage table plus daily aggregate table.
- Large audio/artifacts: object storage with DB metadata.

Do not make sticky LiveKit routing or shared disks the primary solution. They can be temporary mitigations, but they are weaker operationally than centralized durable state.

## Data Model Plan

### Keep Existing Tables

Keep and reuse:

- `ai_device`
- `ai_agent`
- `kid_profile`
- `parent_profile`
- `ai_agent_chat_history`
- `device_token_usage`

But do not overload them beyond their shape. `ai_agent_chat_history` should become a legacy compatibility/backfill source, not the long-term canonical session store.

### Add Runtime Session Tables

Add `voice_sessions`:

- `id uuid primary key`
- `session_id text unique not null`
- `mac_address text not null`
- `device_id uuid references ai_device(id)`
- `agent_id uuid references ai_agent(id)`
- `kid_id bigint references kid_profile(id)`
- `room_name text`
- `worker_id text`
- `status text check in ('active','ended','failed')`
- `started_at timestamptz not null default now()`
- `ended_at timestamptz`
- `last_event_at timestamptz`
- `metadata jsonb not null default '{}'`

Add indexes:

- `(mac_address, started_at desc)`
- `(agent_id, started_at desc)`
- `(kid_id, started_at desc)`
- `(status, last_event_at)`

Add `voice_session_messages`:

- `id uuid primary key`
- `session_id text not null references voice_sessions(session_id)`
- `mac_address text not null`
- `agent_id uuid`
- `sequence integer not null`
- `role text not null`
- `content text`
- `provider_message jsonb`
- `audio_id text`
- `created_at timestamptz not null default now()`
- `idempotency_key text not null`

Add constraints/indexes:

- `unique(session_id, sequence)`
- `unique(idempotency_key)`
- `(mac_address, created_at desc)`
- `(session_id, created_at)`
- `(agent_id, created_at desc)`

### Chat History API Compatibility

Keep the existing app-facing chat-history API contract, but change its backing store.

Existing endpoints should keep their URLs and response shapes:

- `GET /toy/agent/:id/sessions`
- `GET /toy/agent/:id/chat-history/user`
- `GET /toy/agent/:id/chat-history/audio`
- `GET /toy/agent/:id/chat-history/:sessionId`
- `POST /toy/agent/chat-history/report`
- `POST /toy/agent/chat-history/session`

Implementation target:

- New writes go to `voice_sessions` and `voice_session_messages`.
- Existing read endpoints query `voice_session_messages`.
- During migration, read endpoints can fall back to `ai_agent_chat_history` only for sessions not yet backfilled.
- Backfill old `ai_agent_chat_history` rows into `voice_sessions` and `voice_session_messages`.
- After backfill and app verification, stop relying on `ai_agent_chat_history` for app reads.

This gives the app chat history without requiring app changes, while still moving the backend to the correct source of truth.

Add `voice_session_summaries`:

- `id uuid primary key`
- `session_id text not null`
- `mac_address text not null`
- `summary text not null`
- `model text`
- `source_message_count integer`
- `created_at timestamptz not null default now()`
- `updated_at timestamptz not null default now()`
- `unique(session_id)`

This table should become the canonical replacement for local JSONL summaries. Keep `ai_agent.summary_memory` as a legacy or template field, not as the device's primary memory.

### Add Device Memory Tables

The Prisma schema already contains `device_memories` and `memory_chunks`, but the deployed DB does not. Decide whether to adopt that shape or replace it with clearer names. A good production shape:

Add `device_memory_documents`:

- `id uuid primary key`
- `mac_address text not null`
- `kid_id bigint`
- `memory_type text not null`
- `memory_date date`
- `content text not null`
- `source text not null`
- `created_at timestamptz not null default now()`
- `updated_at timestamptz not null default now()`
- `unique(mac_address, memory_type, memory_date)`

Add `device_memory_chunks`:

- `id bigserial primary key`
- `document_id uuid references device_memory_documents(id) on delete cascade`
- `mac_address text not null`
- `kid_id bigint`
- `content text not null`
- `content_hash text not null`
- `category text`
- `embedding vector(...)`
- `created_at timestamptz not null default now()`
- `unique(mac_address, content_hash)`

Before adding vector columns, install and verify the `vector` extension. If Supabase project settings do not allow it, use Qdrant as the vector store and keep only memory document metadata in Postgres.

### Split Usage Tables

Keep `device_token_usage` as a daily aggregate table.

Add `device_token_usage_session`:

- `id bigserial primary key`
- `mac_address text not null`
- `session_id text not null`
- token columns matching `device_token_usage`
- latency and duration columns
- `started_at timestamptz`
- `ended_at timestamptz`
- `created_at timestamptz not null default now()`
- `updated_at timestamptz not null default now()`
- `unique(mac_address, session_id)`

Then update `/toy/usage/tokens/:macAddress/session/:sessionId` to read from `device_token_usage_session`. Continue updating `device_token_usage` as an aggregate for dashboards.

## MQTT Gateway Metadata Plan

The MQTT Gateway is the primary context assembler for LiveKit startup.

Current inspected path:

`D:\cheeko-backend\main\mqtt-gateway`

Key implementation finding:

- `core/mem0-integration.js` already provides `buildDispatchMetadata(...)`.
- `mqtt/virtual-connection.js` already uses `buildDispatchMetadata(...)` when dispatching the conversation agent.
- `gateway/mqtt-gateway.js` still has several direct `metadata: JSON.stringify({...})` dispatch payloads.

Plan:

- Standardize all LiveKit dispatch metadata through `buildDispatchMetadata(...)`.
- Ensure every dispatch path sends the same metadata contract:
  - `device_mac`
  - `device_uuid`
  - `character`
  - `child_profile`
  - `session_language_code`
  - `session_language_name`
  - `session_voice_id`
  - `session_agent_name`
  - `long_term_memories`
  - `memory_relations`
  - `memory_entities`
  - `timestamp`
- Treat this metadata as the worker's primary bootstrap input.
- Keep manager API bootstrap as fallback/debug support, not the normal startup dependency.

## Manager API Plan

### Bootstrap Endpoint

Add:

`GET /toy/agent/device/:mac/bootstrap`

Fallback response should include:

- normalized device identity
- assigned agent config
- kid profile
- current mode/device mode
- recent session summary
- recent messages within a configured budget
- long-term memory blocks
- rendered prompt inputs
- config/version/hash for observability

This endpoint is the recovery/debug path for workers that receive missing or invalid room metadata. It should not be required on the normal MQTT Gateway room creation path.

### Session Lifecycle Endpoints

Add:

- `POST /toy/agent/device/:mac/sessions/start`
- `POST /toy/agent/device/:mac/sessions/:sessionId/messages`
- `PUT /toy/agent/device/:mac/sessions/:sessionId/summary`
- `POST /toy/agent/device/:mac/sessions/:sessionId/end`

Message writes must support:

- monotonic sequence numbers
- idempotency keys
- retry safety
- partial recovery after worker crash

For the first rollout, keep the existing chat-history endpoint contracts, but make them write to and read from `voice_sessions`/`voice_session_messages`. Use `ai_agent_chat_history` only as a temporary migration fallback for older rows.

### Memory Endpoints

Add:

- `GET /toy/agent/device/:mac/memory`
- `POST /toy/agent/device/:mac/memory/documents`
- `POST /toy/agent/device/:mac/memory/search`

The manager API should own retrieval policy:

- short-term recent messages
- session summary
- stable long-term memory
- semantic memory
- kid profile facts

The LiveKit worker should not decide database retrieval logic by itself. It should ask for a prepared context bundle.

### Security

All LiveKit write endpoints must require service-to-service authentication.

Current state is mixed:

- Chat history upload uses a bearer manager API secret from PicoClaw.
- Token usage POST appears public in the route comments and does not require the same service auth.

Plan:

- Require `Authorization: Bearer <manager_api_secret>` for all worker writes.
- Normalize MAC addresses at the boundary.
- Validate that `agent_id` belongs to the given device if provided.
- Use idempotency keys for retries.
- Never expose database credentials to workers or devices.

## PicoClaw Worker Plan

### Replace Persistent Local Workspace With Hydrated Runtime Workspace

Change the LiveKit worker behavior:

- Keep deriving `workspaceIdentity` from MAC.
- Stop relying on the existing local workspace as canonical state.
- On room start, parse and validate room metadata first.
- Render `IDENTITY.md`, `USER.md`, memory context, and runtime config from room metadata when valid.
- Call manager bootstrap only as fallback when room metadata is missing, malformed, stale, or incomplete.
- Log the selected `bootstrap_source` as `room_metadata` or `manager_api_fallback`.
- Use a temporary workspace path that includes `session_id` or worker id.
- Delete local workspace after the session once flush succeeds.

Temporary fallback:

- If metadata is invalid and manager API is unavailable, allow a degraded mode with no long-term memory.
- Log a clear warning and avoid pretending the user has durable continuity.

### Add Manager-Backed SessionStore

Implement a new `session.SessionStore` backend:

- `AddFullMessage` writes to manager API message endpoint.
- `GetHistory` reads recent history from bootstrap cache or manager API.
- `GetSummary` reads from bootstrap cache or manager API.
- `SetSummary` writes to session summary endpoint.
- `TruncateHistory` updates local prompt context and marks historical messages as summarized, but does not delete canonical messages.
- `Save` flushes pending writes.
- `Close` flushes and reports any failed writes.

Because the current `SessionStore` write methods do not return errors, the implementation should maintain an internal retry queue and expose health through logs/metrics. For production correctness, consider extending the interface later to return errors or accept a durable outbox.

### Add Local Outbox For Network Failures

If manager API is temporarily unavailable during a call:

- write pending events to a local encrypted outbox file
- retry with exponential backoff
- flush on session close
- run a background replay loop on worker start

The outbox is not the source of truth. It is a crash-recovery buffer.

## Rollout Phases

### Phase 0: Reconcile Schema

Goals:

- Confirm Prisma schema matches deployed DB.
- Decide whether memory tables should be Prisma's current `device_memories` and `memory_chunks` or the clearer session/memory tables above.
- Add migration baseline for the deployed Supabase/Postgres database.
- Add missing legacy indexes to make backfill/fallback safe:
  - `ai_agent_chat_history(mac_address, created_at desc)`
  - `ai_agent_chat_history(mac_address, session_id)`
- Add indexes for the new canonical read path:
  - `voice_session_messages(agent_id, created_at desc)`
  - `voice_session_messages(mac_address, created_at desc)`
  - `voice_session_messages(session_id, sequence)`

Exit criteria:

- Migration can run against staging.
- Prisma introspection does not show unexpected drift.
- No secrets are committed to repo.
- Existing chat-history API response fixtures are captured before changing the backing store.

### Phase 1: Room Metadata Primary Bootstrap

Goals:

- Treat MQTT Gateway dispatch metadata as the primary startup context for LiveKit workers.
- Standardize metadata payloads built in `D:\cheeko-backend\main\mqtt-gateway`.
- Ensure all LiveKit dispatch paths use the same metadata contract:
  - `device_mac`
  - `device_uuid`
  - `character`
  - `child_profile`
  - `session_language_code`
  - `session_language_name`
  - `session_voice_id`
  - `session_agent_name`
  - `long_term_memories`
  - `memory_relations`
  - `memory_entities`
  - `timestamp`
- Prefer the shared `buildDispatchMetadata(...)` helper from `core/mem0-integration.js` instead of hand-built JSON metadata in scattered dispatch call sites.
- Add `/toy/agent/device/:mac/bootstrap` as fallback/debug support, not as the normal worker startup dependency.
- Keep LiveKit writing `IDENTITY.md`, but build it from room metadata first.

Exit criteria:

- A captured MQTT Gateway dispatch metadata payload can be replayed on two different LiveKit worker instances and produce the same initial `IDENTITY.md`.
- LiveKit worker startup does not require a manager API call when valid room metadata is present.
- If room metadata is missing or malformed, the worker falls back to manager bootstrap and logs the fallback reason.
- All MQTT Gateway dispatch paths use the same metadata contract.
- Existing room creation and character/mode-change flows continue dispatching agents successfully.

### Phase 2: Durable Session Writes

Goals:

- Add `voice_sessions` and `voice_session_messages`.
- Add session start/message/end endpoints.
- Implement manager-backed `SessionStore` in PicoClaw.
- Change existing chat-history APIs to read from `voice_session_messages`.
- Change existing chat-history write APIs to write to `voice_sessions` and `voice_session_messages`.
- Backfill existing `ai_agent_chat_history` rows into the new tables.
- Keep a temporary legacy fallback read from `ai_agent_chat_history` for sessions not yet migrated.

Exit criteria:

- A worker crash mid-call preserves all messages written before the crash.
- Reconnect to another worker can load recent history.
- Duplicate retries do not create duplicate messages.
- The app still sees chat sessions and message transcripts through the current chat-history endpoints.
- Removing the legacy fallback in staging does not change chat-history API responses for migrated sessions.

### Phase 3: Device-Scoped Summary And Memory

Goals:

- Stop using `ai_agent.summary_memory` as the main device memory.
- Add device/kid-scoped summary and memory document tables.
- Add memory extraction job after sessions end.
- Add semantic retrieval with either pgvector or Qdrant.
- Decide how Mem0 fits: primary memory provider, optional enhancer, or deprecated path.

Exit criteria:

- Same device reconnects to a different worker and receives its summary and memory.
- Two devices sharing one agent do not share private memory unless intentionally configured.

### Phase 4: Usage Correctness

Goals:

- Add `device_token_usage_session`.
- Make `/toy/usage/tokens/:mac/session/:sessionId` read exact session usage.
- Keep `device_token_usage` as a daily aggregate updated by job or transaction.

Exit criteria:

- Multiple sessions on the same device/day show correct per-session usage.
- Daily dashboard totals still match the sum of sessions.

### Phase 5: Stateless Worker Mode

Goals:

- Make local workspaces ephemeral by default for LiveKit calls.
- Remove the need for `preserveWorkspace = true` for device continuity.
- Keep only temporary rendered files and local outbox files.

Exit criteria:

- Load-balanced workers can be restarted or replaced with no long-term memory loss.
- Reconnect tests pass across at least two worker instances.

### Phase 6: Observability And Operations

Goals:

- Add metrics for bootstrap latency, message write latency, failed writes, outbox depth, memory retrieval latency, and summary generation latency.
- Add dashboards and alerts.
- Add admin tooling to inspect a device's sessions, summaries, memories, and usage.

Exit criteria:

- On-call can answer: "What did this device remember, from where, and when was it last updated?"
- Failed manager writes produce alerts before users notice continuity loss.

## Concurrency Rules

Decide explicitly whether one device can have two active sessions.

Recommended default:

- One active voice session per physical device.
- Use a `voice_sessions` active record plus transaction or Postgres advisory lock by normalized MAC.
- If a new session starts, mark the old session as interrupted/stale.

If multi-session is required:

- Keep separate session histories.
- Merge summaries only after sessions end.
- Resolve conflicting memory writes through an async memory consolidation job.

## Testing Plan

Minimum tests before production rollout:

- Same MAC connects to worker A, talks, disconnects, reconnects to worker B, and sees continuity.
- Worker A crashes mid-call; worker B reconnect loads messages written before crash.
- Manager API is down during a call; outbox retries and flushes later.
- Same MAC sends duplicate message writes; only one canonical message is stored.
- Two different MACs using the same `agent_id` do not leak summary or memory.
- Multiple sessions on the same day have correct per-session and daily token usage.
- Room metadata bootstrap stays under the voice startup latency budget without requiring a manager API call.
- Manager bootstrap fallback stays available for missing or malformed metadata.
- Existing app chat-history screens work without app-side endpoint changes.

## Immediate Next Actions

1. Standardize MQTT Gateway dispatch metadata using `buildDispatchMetadata(...)` across all dispatch paths.
2. Update PicoClaw LiveKit worker bootstrap logic so room metadata is the primary source for initial identity/context.
3. Add manager bootstrap endpoint as fallback/debug support, not as the normal startup dependency.
4. Add manager API migration for session tables and exact per-session usage.
5. Update existing chat-history APIs so the app reads from `voice_session_messages` through the same routes.
6. Add manager-backed `SessionStore` in PicoClaw behind a config flag.
7. Backfill `ai_agent_chat_history` into the new session tables and keep temporary fallback reads during migration.
8. Run staging with two LiveKit worker instances using the same captured room metadata payload.
9. Only after metadata bootstrap and chat-history API verification, make local workspaces ephemeral for LiveKit device sessions.

## Implementation Progress

Last updated: 2026-04-22

Operational database note:

- The configured Postgres database already had many application tables but did not have Prisma migration history recorded in `_prisma_migrations`.
- `20260124000000_init` was recorded as a baseline migration because replaying it would collide with existing live objects.
- `20260422_add_voice_sessions` was validated in a rollback transaction, applied transactionally, and then recorded as applied in Prisma's migration ledger.
- The remaining additive migrations, `20260124_add_device_audit_columns`, `20260328_add_rfid_category`, and `20260404_add_rfid_card_tap_log`, were validated in rollback transactions and applied through `npx prisma migrate deploy`.
- Current `npx prisma migrate status` result: `Database schema is up to date!`
- Remaining caution: the init migration is a baseline marker, not proof that every old live table exactly matches the checked-in init SQL. Treat future migrations as safe to deploy through Prisma, but do a separate drift audit before refactoring old tables.

1. Standardize MQTT Gateway dispatch metadata using `buildDispatchMetadata(...)` across all dispatch paths.
   - Status: done.
   - Files: `D:\cheeko-backend\main\mqtt-gateway\gateway\mqtt-gateway.js`, `D:\cheeko-backend\main\mqtt-gateway\tests\dispatch-metadata.test.js`.
   - Verification: `node --test tests\dispatch-metadata.test.js`, `node --check gateway\mqtt-gateway.js`, `node --check core\mem0-integration.js`, and `node --check tests\dispatch-metadata.test.js` passed.

2. Update PicoClaw LiveKit worker bootstrap logic so room metadata is the primary source for initial identity/context.
   - Status: done.
   - Files: `D:\picoclaw\cmd\picoclaw-livekit\main.go`, `D:\picoclaw\cmd\picoclaw-livekit\bootstrap_metadata.go`, `D:\picoclaw\cmd\picoclaw-livekit\bootstrap_metadata_test.go`.
   - Verification: `go test -c -o $env:TEMP\picoclaw-livekit.test.exe ./cmd/picoclaw-livekit` passed. With `D:\picoclaw` prepended to `PATH` so Windows can load `ten_vad.dll`, `go test ./cmd/picoclaw-livekit -count=1` also passes.

3. Add manager bootstrap endpoint as fallback/debug support, not as the normal startup dependency.
   - Status: done.
   - Files: `D:\cheeko-backend\main\manager-api-node\src\services\agent.service.js`, `D:\cheeko-backend\main\manager-api-node\src\routes\agent.routes.js`, `D:\cheeko-backend\main\manager-api-node\tests\unit\agent.bootstrap.test.js`.
   - Endpoint: `GET /toy/agent/device/:mac/bootstrap`.
   - Auth: requires `x-service-key` or Bearer service key through the existing `requireServiceKey` middleware because the payload can include child profile, memory, and recent transcript context.
   - Contract: returns `bootstrapSource: manager_api_fallback`, normalized device identity, agent config, child profile, recent chat-history messages, memory payload, and `generatedAt`.
   - Verification: `npx jest tests/unit/agent.bootstrap.test.js --runInBand` passed. `node --check` passed for the touched route, service, and test files. Targeted ESLint still reports pre-existing style violations in untouched portions of `src\services\agent.service.js`; the new route and test do not add lint errors.

4. Add manager API migration for session tables and exact per-session usage.
   - Status: done and applied to the configured Postgres database.
   - Files: `D:\cheeko-backend\main\manager-api-node\prisma\schema.prisma`, `D:\cheeko-backend\main\manager-api-node\prisma\migrations\20260422_add_voice_sessions\migration.sql`, `D:\cheeko-backend\main\manager-api-node\tests\unit\voice-session-schema.test.js`.
   - Schema added: `voice_sessions`, `voice_session_messages`, `voice_session_summaries`, and `device_token_usage_session`.
   - Database verification: live Postgres now contains all four new tables. Current row counts after backfill are `voice_sessions=1`, `voice_session_messages=3`, `voice_session_summaries=0`, and `device_token_usage_session=0`.
   - Migration ledger: `20260422_add_voice_sessions` is now recorded as applied in `_prisma_migrations`.
   - Verification: migration SQL validated in a rollback transaction, then applied transactionally. `npx prisma migrate status` now reports `Database schema is up to date!`. `npx jest tests/unit/voice-session-schema.test.js --runInBand`, `node --check tests\unit\voice-session-schema.test.js`, and `npx prisma validate` passed.

5. Update existing chat-history APIs so the app reads from `voice_session_messages` through the same routes.
   - Status: done.
   - Files: `D:\cheeko-backend\main\manager-api-node\src\services\agent.service.js`, `D:\cheeko-backend\main\manager-api-node\tests\unit\agent.chat-history.voice-session.test.js`.
   - Behavior: `getAgentSessions`, `getChatHistory`, `getRecentUserChatHistory`, and `getAudioContent` prefer `voice_session_messages` and temporarily fall back to `ai_agent_chat_history` when no migrated rows exist. `addChatMessage`, `reportChatMessage`, and `batchUploadSession` now write to `voice_sessions` and `voice_session_messages`.
   - Verification: `npx jest tests/unit/agent.bootstrap.test.js tests/unit/voice-session-schema.test.js tests/unit/agent.chat-history.voice-session.test.js --runInBand`, `node --check` for touched service/route/test files, `npx prisma validate`, `npx prisma generate`, and ESLint for the new unit tests passed. ESLint for `src\services\agent.service.js` still reports older style violations outside this change.

6. Add manager-backed `SessionStore` in PicoClaw behind a config flag.
   - Status: done and locally runtime-verified.
   - Files: `D:\picoclaw\pkg\session\manager_api_backend.go`, `D:\picoclaw\pkg\session\manager_api_backend_test.go`, `D:\picoclaw\pkg\session\session_store.go`, `D:\picoclaw\pkg\config\config.go`, `D:\picoclaw\pkg\config\defaults.go`, `D:\picoclaw\cmd\picoclaw-livekit\manager_session_store.go`, `D:\picoclaw\cmd\picoclaw-livekit\manager_session_store_test.go`, `D:\picoclaw\cmd\picoclaw-livekit\main.go`, `D:\picoclaw\pkg\livekit\agent_bridge.go`, `D:\picoclaw\pkg\livekit\post_session_persistence.go`.
   - Config flag: `livekit_service.manager_api.session_store_enabled` / `PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED`.
   - Runtime config: `livekit_service.manager_api.base_url` / `PICOCLAW_LIVEKIT_MANAGER_API_URL`, `livekit_service.manager_api.recent_limit` / `PICOCLAW_LIVEKIT_MANAGER_RECENT_LIMIT`.
   - Service key source: environment only, in priority order `PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY`, `SERVICE_SECRET_KEY`, `MANAGER_API_SECRET`.
   - Behavior: the store hydrates recent device context from manager bootstrap, caches it locally for fast reads, writes user/assistant turns to Manager API during the call, persists summaries through the manager memory endpoint, and marks real-time chat persistence so LiveKit skips duplicate end-of-session chat-history upload while still sending usage.
   - Runtime enablement fix: the worker also checks `PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED` and `PICOCLAW_LIVEKIT_MANAGER_API_URL` directly at startup, so manager-backed persistence can be enabled even if nested config env parsing does not populate `livekit_service.manager_api`.
   - Verification: with `D:\picoclaw;C:\msys64\mingw64\bin` prepended to `PATH`, `go test ./cmd/picoclaw-livekit ./pkg/livekit ./pkg/session -count=1` passed.
   - Credential-path verification: a local worker launched from `D:\picoclaw` registered to LiveKit using `C:\Users\rahul\.picoclaw\.security.yml` for LiveKit service credentials, loaded manager settings from `.env`, accepted a dispatched room, logged `bootstrap_source=room_metadata`, and logged `Using manager-backed session store`.

7. Backfill `ai_agent_chat_history` into the new session tables and keep temporary fallback reads during migration.
   - Status: done and executed against the configured Postgres database.
   - Files: `D:\cheeko-backend\main\manager-api-node\scripts\backfill-ai-agent-chat-history-to-voice-sessions.js`, `D:\cheeko-backend\main\manager-api-node\tests\unit\backfill-chat-history.test.js`, `D:\cheeko-backend\main\manager-api-node\package.json`.
   - Commands: `npm run backfill:chat-history:dry` for dry run, `npm run backfill:chat-history` for execution.
   - Behavior: groups legacy rows by `session_id`, upserts one `voice_sessions` row per session, inserts idempotent `voice_session_messages` rows with stable `legacy:ai_agent_chat_history:<id>` keys, preserves sequence by legacy `created_at`/`id`, and supports dry-run summaries.
   - Execution result: dry run reported `sessions=1`, `messages=3`; real backfill inserted 3 messages; a second real run inserted 0 messages, confirming idempotency.
   - Verification: `npx jest tests/unit/agent.bootstrap.test.js tests/unit/voice-session-schema.test.js tests/unit/agent.chat-history.voice-session.test.js tests/unit/backfill-chat-history.test.js --runInBand`, `npx prisma validate`, `node --check scripts\backfill-ai-agent-chat-history-to-voice-sessions.js`, and ESLint for the new backfill script/test passed.

8. Run staging with two LiveKit worker instances using the same captured room metadata payload.
   - Status: done for local two-worker runtime verification.
   - Windows runtime requirement: `D:\picoclaw` must be present on `PATH` before launching `go test` or the `picoclaw-livekit` worker so Windows can load `D:\picoclaw\ten_vad.dll`. Without that, runtime tests fail with exit `0xc0000135`.
   - Local config check: `D:\picoclaw\.env` has manager API URL and secret values, Manager API has `SERVICE_SECRET_KEY`, and `PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED=true` is now enabled locally.
   - Local manager verification: `GET /toy/agent/device/:mac/bootstrap?includeMemories=false&recentLimit=5` succeeds with service-key auth and returns device plus agent context for a real MAC.
   - Local runtime verification: with `D:\picoclaw;C:\msys64\mingw64\bin` prepended to `PATH`, `go test ./cmd/picoclaw-livekit ./pkg/livekit ./pkg/session -count=1` passes.
   - Single-worker credential verification: the normal launch path reads LiveKit credentials from `.security.yml`, manager auth/config from `.env`, and successfully starts a manager-backed room session.
   - Two-worker dispatch verification: Worker A accepted `local-continuity-...-one`; after Worker A was stopped, Worker B accepted `local-continuity-...-two` for the same MAC. Both logs showed `bootstrap_source=room_metadata`, `workspace_identity=device-28562f07d3ac`, and `Using manager-backed session store`.
   - Operational note: launching two local workers with the same `health_port` causes the second worker health server to fail binding `8192`, but the worker still registers and accepts LiveKit jobs. Production should assign unique health ports per process/container or run one worker per container.
   - Remaining staging input: repeat the same test in the real load-balanced environment after deployment packaging includes the DLL search path and manager session-store env values.

9. Add session-end, summary, and exact per-session usage persistence.
   - Status: implemented and code-verified.
   - Files: `D:\cheeko-backend\main\manager-api-node\src\services\agent.service.js`, `D:\cheeko-backend\main\manager-api-node\src\routes\agent.routes.js`, `D:\cheeko-backend\main\manager-api-node\src\services\device.service.js`, `D:\cheeko-backend\main\manager-api-node\src\routes\device.routes.js`, `D:\cheeko-backend\main\manager-api-node\tests\unit\agent.voice-session-lifecycle.test.js`, `D:\cheeko-backend\main\manager-api-node\tests\unit\device.token-usage-session.test.js`, `D:\picoclaw\pkg\livekit\agent_bridge.go`, `D:\picoclaw\pkg\livekit\post_session_persistence.go`, `D:\picoclaw\pkg\livekit\post_session_persistence_test.go`.
   - Manager API behavior: added service-key protected `PUT /toy/agent/device/:mac/sessions/:sessionId/summary` and `POST /toy/agent/device/:mac/sessions/:sessionId/end`.
   - Summary behavior: session summaries are written to `voice_session_summaries` and also update `ai_agent.summary_memory` so existing bootstrap payloads can include the latest durable summary.
   - Usage behavior: `POST /toy/device/token-usage` now accepts `totalTokens`, writes exact per-session rows to `device_token_usage_session`, and updates the daily aggregate `device_token_usage.total_tokens`.
   - PicoClaw behavior: post-session persistence now sends total tokens, finalizes a session summary at disconnect even when the rolling context threshold was not reached, sends the session summary to Manager API, and marks the session ended.
   - Verification: `npx jest tests/unit/agent.bootstrap.test.js tests/unit/voice-session-schema.test.js tests/unit/agent.chat-history.voice-session.test.js tests/unit/backfill-chat-history.test.js tests/unit/agent.voice-session-lifecycle.test.js tests/unit/device.token-usage-session.test.js --runInBand --forceExit`, `node --check` for touched manager files/tests, `npx prisma validate`, `go test ./cmd/picoclaw-livekit ./pkg/livekit ./pkg/session -count=1`, and `git diff --check` passed.
   - Follow-up fix from live run: LiveKit now sends both `Authorization: Bearer ...` and `X-Service-Key` for Manager API service calls, because `requireServiceKey` only accepts `X-Service-Key`. Manager API also retries server-generated message sequence assignment when concurrent STT finalization races produce the same `(session_id, sequence)`.
   - Runtime note: the currently running Manager API and LiveKit worker must be restarted/rebuilt before the latest auth/sequence fixes appear in live device sessions.

10. Fix LiveKit voice workspace tool contract.
   - Status: done.
   - Files: `D:\picoclaw\pkg\agent\instance.go`, `D:\picoclaw\pkg\agent\instance_test.go`, `D:\picoclaw\cmd\picoclaw-livekit\workspace_tools.go`, `D:\picoclaw\cmd\picoclaw-livekit\workspace_tools_test.go`, `D:\picoclaw\cmd\picoclaw-livekit\main.go`, `D:\picoclaw\pkg\livekit\audio_pipeline.go`, `D:\picoclaw\pkg\livekit\audio_pipeline_test.go`.
   - Root cause from live log: local config had `tools.read_file.enabled=false` and `tools.list_dir.enabled=false`, while the LiveKit prompt still required workspace memory and skills through `read_file`. The worker correctly created `workspace-device-00163eacb538`, but the LLM could not access it because the required tools were absent.
   - Follow-up root cause from the 17:28 live run: shared default-agent tools were copied onto the LiveKit device agent after creation and overwrote the device-scoped file tools with tools rooted at the default workspace. That is why `flower_song.txt` was written to `C:\Users\rahul\.picoclaw\workspace\flower_song.txt` and the later device workspace lookup could not find it.
   - Behavior: normal agents still honor global tool toggles, but the LiveKit voice worker now always replaces `read_file`, `write_file`, and `list_dir` with sandboxed tools rooted at the device workspace after shared tools are merged. `exec` is not forced and remains controlled by config. Voice TTS also strips provider channel markers like `<|channel>thought <channel|>` before synthesis.
   - Recovery action: copied the misplaced local `flower_song.txt` from the default workspace into `C:\Users\rahul\.picoclaw\workspace-device-00163eacb538\flower_song.txt` so the immediate device retest can find it.
   - Verification: `go test ./pkg/agent -run 'TestRegisterWorkspaceToolsForcesRequiredFileToolsWhenDisabled|TestNewAgentInstance_AllowsMediaTempDirForReadListAndExec|TestNewAgentInstance_InvalidExecConfigDoesNotExit' -count=1`, `go test ./cmd/picoclaw-livekit -run 'TestEnsureLiveKitWorkspaceFileTools(AddsRequiredToolsWhenConfigDisablesThem|ReplacesDefaultWorkspaceTools)' -count=1` with `D:\picoclaw` on `PATH`, `go test ./pkg/livekit -run TestSanitizeVoiceTextForTTSDropsProviderChannelMarkers -count=1` with `D:\picoclaw` on `PATH`, `go test ./cmd/picoclaw-livekit ./pkg/livekit ./pkg/session -count=1` with `D:\picoclaw` on `PATH`, and targeted `pkg/tools` filesystem tests passed.

11. Only after metadata bootstrap and chat-history API verification, make local workspaces ephemeral for LiveKit device sessions.
   - Status: not started.

## Senior Engineering Recommendation

The proper long-term solution is not to copy the workspace between instances. It is to make the workspace reproducible from durable state.

Local files are useful for fast prompt assembly and sandboxed tool execution, but device memory, identity, summaries, sessions, and usage need canonical records in Postgres through the manager API. Once that is true, any worker can spin up a fresh workspace for the device, hydrate it in milliseconds, and continue the conversation without caring which instance handled the previous call.
