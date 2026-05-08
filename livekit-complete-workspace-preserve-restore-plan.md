# Complete Workspace Preserve + Restore Implementation Plan

**Goal:** Preserve the full per-device workspace (not only 5 files), restore it deterministically on next room join, and prevent data loss on network failures or duplicate sessions without degrading voice UX latency.

**Architecture:** Use `device_workspace_artifacts` as the durable file store for all workspace files, plus a manifest-driven sync protocol (revisioned snapshot + deletions). LiveKit becomes a sync client with outbox retry and per-device lock.

**Repos in scope:**
- `D:\picoclaw`
- `D:\cheeko-backend\main\manager-api-node`

---

## 1. Scope and Design Decisions (P0)

- Use existing table `device_workspace_artifacts` (no new table initially).
- Add a manifest artifact path: `.picoclaw/workspace-manifest.json`.
- Preserve all workspace files except explicit deny-list paths (`trace/`, transient logs, binaries, temp).
- Restore order: fast path first (`summary + recent messages + critical workspace files`), then background full restore (`manifest + remaining files + prune deletes`).
- Keep current `/workspace-files` endpoint for compatibility, but move LiveKit to new full-workspace API.
- Enforce non-blocking persistence: no STT/LLM/TTS turn should wait for remote persistence writes.

**Files to update**
- [workspace.service.js](D:/cheeko-backend/main/manager-api-node/src/services/workspace.service.js)
- [agent.routes.js](D:/cheeko-backend/main/manager-api-node/src/routes/agent.routes.js)
- [workspace_sync.go](D:/picoclaw/cmd/picoclaw-livekit/workspace_sync.go)
- [manager_artifact_store.go](D:/picoclaw/cmd/picoclaw-livekit/manager_artifact_store.go)

---

## 2. Manager API: Full Workspace Sync API (P0)

### Task 2.1: Add batch upload endpoint for full workspace
- Endpoint: `PUT /agent/device/:mac/workspace-sync`
- Request body:
  - `baseRevision`
  - `newRevision`
  - `files`: array of `{relativePath, content, contentType, sha256, sizeBytes}`
  - `deleted`: array of relative paths
  - `manifest`
- Behavior:
  - Validate all paths with existing `normalizeWorkspaceRelativePath` rules.
  - Upsert files to `device_workspace_artifacts`.
  - Delete rows for `deleted` paths (same `mac`).
  - Store/overwrite manifest as artifact at `.picoclaw/workspace-manifest.json`.
  - Return `appliedRevision`, `savedCount`, `deletedCount`.

### Task 2.2: Add restore endpoint
- Endpoint: `GET /agent/device/:mac/workspace-sync?sinceRevision=<rev>&limit=<n>`
- Returns:
  - latest `manifest`
  - delta file list when `sinceRevision` is present; full snapshot when absent
  - `revision`

### Task 2.3: Add optimistic concurrency check
- If `baseRevision` mismatches server manifest revision, return `409` with server revision.
- This prevents stale client overwrite.

### Task 2.4: Add full session-history read API for lazy restore
- Endpoint: `GET /agent/device/:mac/sessions/:sessionId/messages?cursor=<cursor>&limit=<n>`
- Behavior:
  - return chronological, paginated full message history
  - include next cursor
  - preserve existing recent bootstrap API for fast path

**Files**
- [workspace.service.js](D:/cheeko-backend/main/manager-api-node/src/services/workspace.service.js)
- [agent.routes.js](D:/cheeko-backend/main/manager-api-node/src/routes/agent.routes.js)
- [agent.workspace-artifacts.test.js](D:/cheeko-backend/main/manager-api-node/tests/unit/agent.workspace-artifacts.test.js)

---

## 3. LiveKit Agent: Complete Snapshot + Restore Client (P0)

### Task 3.1: Add workspace scanner + manifest builder
- Scan workspace recursively.
- Exclude deny-list:
  - `trace/**`
  - `*.log`
  - `.picoclaw/sync-outbox/**`
  - large binary extensions (configurable).
- Build manifest:
  - `revision`
  - `generatedAt`
  - `files[]` with path/hash/size/mtime
  - `deleted[]` (vs previous manifest).

### Task 3.2: Add restore flow before session runtime
- On room assignment:
  - do fast restore first: summary + recent N + critical files (`AGENT.md`, `USER.md`, `SOUL.md`, `memory/MEMORY.md`)
  - start greeting/listening after fast restore completes
  - run background full restore: fetch manifest delta/files, write safely, prune deletes
  - lazy-load older full session-history pages only when needed (not on critical greeting path)

### Task 3.3: Add upload flow on close and periodic checkpoint
- Trigger upload:
  - on graceful close
  - every N minutes during long sessions (configurable)
- Upload with `baseRevision/newRevision` and changed-file delta only (not full snapshot each time).
- On `409`, do merge-reload-upload retry once.

**Files**
- [workspace_sync.go](D:/picoclaw/cmd/picoclaw-livekit/workspace_sync.go)
- [main.go](D:/picoclaw/cmd/picoclaw-livekit/main.go)
- [workspace_artifacts.go](D:/picoclaw/pkg/livekit/workspace_artifacts.go)
- [manager_artifact_store.go](D:/picoclaw/cmd/picoclaw-livekit/manager_artifact_store.go)

---

## 4. Data-Loss Protection (P0)

### Task 4.1: Outbox for failed uploads
- If sync API unavailable, persist payload to local outbox:
  - `workspace/state/workspace-sync-outbox.jsonl`
- Retry on next startup and periodic ticker until success.

### Task 4.2: Safe-delete policy for ephemeral workspaces
- Do not delete workspace if:
  - upload failed
  - outbox not empty
- Delete only after successful sync or explicit force flag.

**Files**
- [agent_bridge.go](D:/picoclaw/pkg/livekit/agent_bridge.go)
- [main.go](D:/picoclaw/cmd/picoclaw-livekit/main.go)

---

## 5. Concurrency Safety (P1)

### Task 5.1: Per-device workspace lock
- Lock file path:
  - `workspace/.picoclaw/device.lock`
- Lock acquired at session start; released on leave.
- If lock is already held:
  - reject duplicate room session for same device or wait with timeout.
- Include stale-lock recovery using PID + heartbeat timestamp.

**Files**
- New: `cmd/picoclaw-livekit/workspace_lock.go`
- [worker.go](D:/picoclaw/pkg/livekit/worker.go)
- [room_session.go](D:/picoclaw/pkg/livekit/room_session.go)

---

## 6. Config + Observability (P1)

### Task 6.1: Runtime config knobs
- `workspace_sync.enabled`
- `workspace_sync.interval_seconds`
- `workspace_sync.max_file_bytes`
- `workspace_sync.exclude_patterns`
- `workspace_sync.outbox_retry_seconds`
- `workspace_sync.lock_timeout_seconds`
- `workspace_restore.fast_path_timeout_ms`
- `workspace_restore.background_enabled`
- `workspace_restore.history_page_size`
- `workspace_restore.max_history_pages_on_idle`

### Task 6.2: Metrics/logs
- `workspace_restore_duration_ms`
- `workspace_files_restored`
- `workspace_sync_saved_count`
- `workspace_sync_deleted_count`
- `workspace_sync_conflict_count`
- `workspace_sync_outbox_size`
- `workspace_restore_fast_path_ms`
- `workspace_restore_background_ms`
- `workspace_restore_history_pages_loaded`
- `first_greeting_ready_ms`

**Files**
- [config.go](D:/picoclaw/pkg/config/config.go)
- [config.example.json](D:/picoclaw/config/config.example.json)
- [main.go](D:/picoclaw/cmd/picoclaw-livekit/main.go)

---

## 7. Performance Guardrails (P0/P1)

### Task 7.1: Define latency SLOs and enforcement
- Fast restore SLO: target < 500ms local, < 1200ms with manager API available.
- First greeting readiness SLO: should not regress by more than 300ms from current baseline.
- Background restore must be cancel-safe and interrupt-safe.

### Task 7.2: Ensure all durability work is async off critical path
- Realtime message persistence uses outbox + retry and never blocks turn completion.
- Full history restore is paginated and idle-driven.
- Workspace sync uses delta by hash/revision.

---

## 8. Test Plan (P0/P1)

### Manager API tests
- Add/extend:
  - [agent.workspace-artifacts.test.js](D:/cheeko-backend/main/manager-api-node/tests/unit/agent.workspace-artifacts.test.js)
  - [agent.voice-session-lifecycle.test.js](D:/cheeko-backend/main/manager-api-node/tests/unit/agent.voice-session-lifecycle.test.js)
- Cases:
  - full upload + restore roundtrip
  - delete propagation
  - path traversal rejection
  - 409 revision conflict

### LiveKit tests
- Add/extend:
  - [workspace_sync_test.go](D:/picoclaw/cmd/picoclaw-livekit/workspace_sync_test.go)
  - [manager_artifact_store_test.go](D:/picoclaw/cmd/picoclaw-livekit/manager_artifact_store_test.go)
  - [workspace_lifecycle_test.go](D:/picoclaw/cmd/picoclaw-livekit/workspace_lifecycle_test.go)
- Cases:
  - restore writes complete tree
  - fast path completes before greeting trigger
  - background restore does not block turns
  - delta sync uploads only changed files
  - failed sync writes outbox
  - safe-delete blocked on failed sync
  - lock prevents concurrent writer

### E2E voice checks (manual)
- Join room, modify `USER.md`, `memory/MEMORY.md`, create a file in `skills/notes.md`.
- End room, restart agent, rejoin same device.
- Verify all changes restored.
- Verify greeting timing remains within SLO after enabling full preserve/restore.
- Verify full history can be fetched on demand without initial join delay.
- Run concurrent duplicate join to ensure lock behavior.
- Simulate Manager API downtime and verify outbox replay on recovery.

---

## 9. Rollout Plan

1. Deploy Manager API endpoints first (backward compatible).
2. Enable LiveKit sync in shadow mode (restore only, no upload).
3. Enable fast-path + background restore split.
4. Enable delta upload with outbox; monitor conflict/error/latency metrics.
5. Enable lock enforcement.
6. Deprecate old 5-file-only sync path after stable period.

---

## 10. Acceptance Criteria

- Full workspace (configured include-set) survives process restarts and room churn.
- No workspace loss when Manager API is temporarily down.
- No concurrent corruption for same device.
- Restore is deterministic with fast-path readiness before first greeting.
- Full-history restore is available and paginated without blocking initial voice turn.
- Latency SLOs remain within defined budget after rollout.
- Existing flows (`/workspace-files`, chat history persistence, session summary) remain compatible.
