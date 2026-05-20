LiveKit Voice Agent on EKS: Workspace + Scaling Implementation Plan
1. Scope
This plan covers productionizing picoclaw-livekit on AWS EKS with:

Global per-device_mac lock in Manager DB (PostgreSQL row lease).
Authoritative workspace state in Manager API/DB; pod workspace is ephemeral cache.
Correct reconnect behavior across pods under autoscaling.
Safe workspace hydration/sync/teardown semantics.
Config/security loading edge cases (config.json + .security.yml).
Sprint-ready delivery plan (P0/P1/P2) with acceptance criteria.
2. Final Architecture Decisions (Locked)
Workspace authority:
Manager API/DB is source of truth.
Pod-local workspace is disposable cache.
First-time user workspace bootstraps from template.
Workspace identity:
Primary key: device_mac.
Locking model:
Global lock backend: PostgreSQL row lease.
Dedicated table: workspace_locks with:
device_mac (PK)
holder_id
lease_expires_at
fencing_token
heartbeat_at
Timings:
heartbeat = 5s
lease_ttl = 20s
acquire_wait_timeout = 25s
stale steal condition: now() > lease_expires_at + 2s
Acquire timeout behavior: fail fast (JS_FAILED) and let LiveKit redispatch.
Bootstrap/migrations:
One-time idempotent bootstrap under same lock.
Persist workspace_initialized_at, template_version.
Template upgrades: manual migration jobs only.
Slow restore behavior:
If hydrate exceeds 3s, start with minimal template + recent summary.
Continue full restore async.
Non-breaking updates apply on next turn in same session.
Major updates force reconnect:
AGENT.md / system prompt replacement
language policy lock change
Session durability policy:
Accept possible loss of in-flight session data.
Persist transcript/session artifacts only on session end.
Workspace sync loop:
checkpoint interval: 180s
retry interval: 30s
Warming-state tool guard:
While warming, block:
write_file
edit_file
delete_file
move_file
mutating exec
stateful cron mutations (add/remove/enable/disable)
Do not auto-queue blocked commands; ask user to retry.
Log structured code tool_blocked_warming.
Conflict handling:
On stale-writer conflict:
Reject write with conflict.
Refresh from DB.
Replay pending outbox.
Retry once.
Fail job if still conflicted.
Outbox behavior:
Dead-letter enabled.
max_outbox_replay_attempts = 5.
On exceed: graceful session end after warning.
Quotas:
Soft workspace limit: 20 MB.
Hard workspace limit: 40 MB.
Per-file cap: 256 KB.
Kubernetes/EKS policy:
max_sessions = 12 per pod.
Resources:
requests: cpu: 2000m, memory: 4Gi, ephemeral-storage: 10Gi
limits: cpu: 4000m, memory: 8Gi
HPA:
primary metric: active_sessions / max_sessions, target 50%
secondary metric: CPU target 60%
scaleUp.stabilizationWindowSeconds = 0
scaleDown.stabilizationWindowSeconds = 900
default rate policies initially
minReplicas = 2, maxReplicas = 5
PDB:
minAvailable = 1 initially
use 50% when replicas > 2
Rollouts:
RollingUpdate, maxUnavailable=0, maxSurge=1
Shutdown/drain:
terminationGracePeriodSeconds = 900
explicit drain mode: readiness false + availability false immediately, then drain active sessions.
Observability:
Logs-only initially (no metrics yet).
Structured reason codes required.
3. Config/Security Edge Case Plan (config.json + .security.yml)
3.1 Current Runtime Load Path
Worker does best-effort .env load.
Config path resolution:
--config flag
else PICOCLAW_CONFIG
else PICOCLAW_HOME/config.json
else ~/.picoclaw/config.json
LoadConfig reads config.json.
.security.yml is loaded from same directory as config.json.
Security values are merged/applied into runtime config (including LiveKit credentials).
Environment variables can override security values via tagged fields.
3.2 Production Guardrails to Add
Strict startup mode for picoclaw-livekit:
Missing/unreadable/invalid config.json must fail startup (no silent default in prod mode).
Co-location enforcement:
Validate .security.yml at dirname(config.json)/.security.yml.
Invalid YAML or unreadable file => fail startup with clear structured log.
Startup credential validation:
After merge/apply, validate required LiveKit security values are present:
API key
API secret
required STT/TTS/provider credentials for selected config
Fail fast if unresolved.
Deployment contract:
Mount config.json and .security.yml into same directory.
Always pass explicit --config /mounted/path/config.json.
Logging:
Emit sanitized startup diagnostics showing credential source precedence (without secret values).
4. Implementation Steps by Phase
Phase A - Concurrency Correctness + Startup Safety
Add Manager lock API and DB lease behavior.
Integrate DB lock acquire/heartbeat/release in worker bridge/session lifecycle.
Add dependency health checks used by readiness + availability.
Add drain mode for preStop and shutdown.
Add strict config/security startup validation path.
Phase B - Workspace Lifecycle Robustness
Add workspace state machine (warming, ready, warming_degraded).
Enforce warming tool block list.
Implement conflict + fencing refresh/retry flow.
Add outbox max-attempt + dead-letter policy.
Enforce workspace quota checks.
Phase C - EKS Operations + Test Hardening
Deploy HPA/PDB/resources/rollout/drain configuration.
Add structured log codes across lock/sync/drain/warming flows.
Execute reconnect and failure-injection test suite.
Cut release after acceptance criteria pass.
5. Sprint Board (P0/P1/P2) with Acceptance Criteria
P0 (Must-Have Before Production)
P0-1 Global DB Lock Integration
Acceptance criteria:

Worker enforces single writer per device_mac across pods.
Lease timings match: 5s/20s/25s/+2s.
Lock takeover works after crash/reconnect.
P0-2 Strict Config/Security Startup
Acceptance criteria:

If --config path missing/invalid => pod exits non-zero.
.security.yml parse/read failures fail startup.
Required LiveKit creds unresolved => startup failure.
P0-3 Dependency-Aware Readiness + Availability
Acceptance criteria:

/ready fails when lock DB or Manager storage is unhealthy.
Availability response returns Available=false under same conditions.
P0-4 Explicit Drain Mode
Acceptance criteria:

preStop flips drain flag.
New assignments blocked immediately.
Active sessions drain until completion or grace timeout.
P0-5 Warming Tool Block
Acceptance criteria:

High-risk tool list blocked while warming.
Agent asks retry, no auto-queue.
Structured log tool_blocked_warming emitted.
P0-6 Conflict + Fencing Write Recovery
Acceptance criteria:

Conflict triggers refresh + outbox replay + one retry.
Persistent conflict fails job deterministically.
P0-7 Outbox Dead-Letter Policy
Acceptance criteria:

Replay cap enforced at 5.
Failed payload moved to dead-letter location.
Session gracefully ends after warning when cap exceeded.
P0-8 Quota Enforcement
Acceptance criteria:

Soft 20 MB, hard 40 MB, file cap 256 KB enforced in sync path.
Violations logged with structured reason codes.
P0-9 EKS Baseline Manifests
Acceptance criteria:

HPA/PDB/resources/rollout/grace-period settings match locked decisions.
max_sessions=12, minReplicas=2, maxReplicas=5 active in deployment.
P1 (Stability + Operability)
P1-1 Workspace State Machine Completion
Acceptance criteria:

>3s restore enters degraded start.
Full async restore transitions to ready.
Non-breaking updates apply next turn; major updates force reconnect.
P1-2 Session-End Persistence Validation
Acceptance criteria:

Session-end save path validated for success/failure branches.
Fallback logs and outbox markers are correct.
P1-3 Shared DB Pool Guardrails
Acceptance criteria:

Lock queries use short timeouts/retries.
Under load tests, heartbeat misses remain within tolerated bounds.
P1-4 Structured Log Taxonomy
Acceptance criteria:

Log reason codes standardized for lock/sync/drain/conflict/warming/unhealthy events.
P2 (Optimization + Future Hardening)
P2-1 Runbooks
Acceptance criteria:

Operator docs for lock contention, security file issues, dead-letter recovery, and drain incidents.
P2-2 Chaos/Failure Drills
Acceptance criteria:

Simulated pod kill, DB outage, reconnect race, stale lock takeover all match expected behavior.
P2-3 Capacity Re-Tuning
Acceptance criteria:

Post-launch review adjusts HPA and max_sessions using real workload logs.
6. Recommended Structured Log Codes
config_load_failed
security_load_failed
security_credential_unresolved
drain_mode_enabled
availability_dependency_unhealthy
lock_acquire_timeout
lock_heartbeat_miss
lock_stale_takeover
tool_blocked_warming
workspace_sync_conflict
workspace_sync_retry_failed
outbox_dead_lettered
workspace_quota_soft_exceeded
workspace_quota_hard_exceeded
7. Rollout Sequence
Deploy strict startup + DB lock + drain mode to staging.
Run reconnect race tests with autoscaling enabled.
Validate degraded-start and warming tool blocking behavior.
Validate conflict + outbox dead-letter paths.
Roll to production with low traffic slice.
Expand traffic after log-health checks pass.
8. Open Deferred Items (Intentionally Not in Current Scope)
Prometheus metrics and alerting.
Network hardening (private-only Manager API path).
Region sharding and region-local Manager DB.
Event-based/diff-based workspace persistence.