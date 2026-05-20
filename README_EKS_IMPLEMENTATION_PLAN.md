# LiveKit PicoClaw Agent on AWS EKS: Implementation Plan

## 1) Goal
Deploy `picoclaw-livekit` on AWS EKS with production-grade scaling and reliability, while preserving per-device workspace state in Manager API/DB and preventing data corruption under multi-pod concurrency.

## 2) Repositories in Scope
- Agent runtime: `D:\picoclaw`
- Manager API: `D:\cheeko-backend\main\manager-api-node`

## 2.1) Deployment Targets for This Rollout
- Agent name (LiveKit worker): `cheeko-picoclaw`
- LiveKit server URL: `ws://64.227.170.31:7880`
- LiveKit API key: `key1`
- LiveKit API secret: `secret1`
- Manager API URL: `http://64.227.170.31:8002/toy`

Important:
- Store credentials only in Kubernetes Secrets (never in Git).
- For production internet traffic, prefer `wss://` and `https://` endpoints.

## 3) Success Criteria
- Agent pods scale horizontally on EKS with no duplicate writer corruption for same `device_mac`.
- Workspace state remains durable in DB and recoverable after pod restarts.
- `config.json` and `.security.yml` load consistently and safely in Kubernetes.
- Graceful drain works during rolling deploys and scale-down.
- Observability is sufficient to diagnose lock, sync, conflict, and restore failures.

## 4) Current Facts (from code)
- Agent already supports workspace sync + conflict retry + outbox in `cmd/picoclaw-livekit/workspace_sync.go`.
- Manager API stores workspace artifacts in `device_workspace_artifacts`.
- Current workspace lock is local file lock (`.picoclaw/device.lock`) and is not cluster-wide.
- Config path resolution uses `--config` -> `PICOCLAW_CONFIG` -> `PICOCLAW_HOME/config.json` -> `~/.picoclaw/config.json`.
- `.security.yml` is loaded from same directory as `config.json`.

## 5) Target Architecture
- Source of truth for workspace: Manager API + Postgres.
- Pod workspace: ephemeral cache (`emptyDir`) only.
- Concurrency control: distributed DB lock keyed by `device_mac` with lease + fencing token.
- Workspace writes: transactional revision check + write + manifest update.
- Startup config mode: strict in production (fail fast on missing/invalid config/security).

## 5.1) Capacity and Scaling Decisions (Locked)
- Per instance (per pod) max concurrent agent sessions: `12` (`PICOCLAW_LIVEKIT_MAX_SESSIONS=12`).
- Scale metric alignment with LiveKit guidance:
  - Worker admission metric: session capacity (`activeSessions < maxSessions`).
  - HPA primary metric: `activeSessions / maxSessions`.
  - HPA scale-up target: `0.50` (50%), which is intentionally below worker saturation.
- CPU secondary metric target: `60%`.
- HPA behavior:
  - scale-up stabilization: short (`0-30s` range, prefer immediate response).
  - scale-down stabilization: long (`600-900s`) to allow conversation drain.
- Pod resources (production baseline):
  - requests: `cpu: 4`, `memory: 8Gi`
  - limits: `cpu: 4`, `memory: 8Gi`
- Notes:
  - This matches LiveKit starting guidance (`4 cores / 8GB`) for voice workloads.
  - If cost pressure requires lower requests in staging, do that only outside production.

## 6) Implementation Phases

## Phase 0: Design Freeze and Baseline
- [ ] P0.1 Freeze lock lease values, sync intervals, and HPA thresholds.
- [ ] P0.2 Define environment matrix for `dev`, `staging`, `prod`.
- [ ] P0.3 Finalize API contracts for lock and workspace sync conflict responses.
- [ ] P0.4 Freeze worker/session sizing values:
  - `max_sessions=12` per pod.
  - HPA session target `50%`, CPU target `60%`.
  - production resources `4 CPU / 8Gi`.

Acceptance:
- Written constants and contracts are approved and versioned.

## Phase 1: Manager API Hardening (Concurrency + Data Integrity)
- [ ] P1.1 Add `workspace_locks` table migration:
  - `device_mac` (PK), `holder_id`, `fencing_token`, `lease_expires_at`, `heartbeat_at`, `created_at`, `updated_at`.
- [ ] P1.2 Implement lock service methods:
  - acquire, heartbeat, release, steal-stale-lock, verify-fencing.
- [ ] P1.3 Add service-key-only endpoints for lock lifecycle.
- [ ] P1.4 Make `saveWorkspaceSync` transaction-safe:
  - `SELECT ... FOR UPDATE` on manifest row.
  - validate `baseRevision`.
  - apply upserts/deletes + manifest update atomically.
- [ ] P1.5 Add idempotency guard for duplicate sync payloads (hash + revision check).
- [ ] P1.6 Add structured reason codes for conflict, stale lock, invalid payload, quota reject.
- [ ] P1.7 Add unit/integration tests for parallel writes and lock races.

Acceptance:
- No workspace corruption when two writers race for same `device_mac`.
- Stale writer gets deterministic conflict/fencing failure.

## Phase 2: Agent Runtime Hardening (Distributed Lock + Drain + Strict Config)
- [ ] P2.1 Replace local-only lock as primary guard with DB distributed lock.
- [ ] P2.2 Keep local file lock as optional secondary in-pod guard only.
- [ ] P2.3 Acquire lock at session start, heartbeat while session active, release on close.
- [ ] P2.4 On lock acquire timeout, fail job fast so LiveKit can redispatch.
- [ ] P2.5 Implement explicit draining mode:
  - mark unavailable for new jobs on SIGTERM.
  - let active jobs finish until timeout.
- [ ] P2.6 Add strict startup mode:
  - fail if `config.json` missing/unreadable.
  - fail if `.security.yml` invalid/unreadable.
  - fail if required credentials unresolved after merge/env overrides.
- [ ] P2.7 Add startup diagnostics log (redacted) showing config source precedence.
- [ ] P2.8 Add dependency-readiness checks for Manager API and DB lock backend.

Acceptance:
- During rolling update, no abrupt session drops except timeout-expired sessions.
- Production startup fails fast on bad config/security.

## Phase 3: Kubernetes / EKS Platform Rollout
- [ ] P3.1 Build production image for `picoclaw-livekit`.
- [ ] P3.2 Create Deployment manifest:
  - `--config /etc/picoclaw/config.json`
  - mount `config.json` and `.security.yml` in same directory.
  - `emptyDir` workspace cache mount.
- [ ] P3.3 Add probes:
  - startup/readiness/liveness aligned with agent health endpoints.
- [ ] P3.4 Add graceful termination:
  - preStop hook to trigger drain flag.
  - high `terminationGracePeriodSeconds` (voice sessions are long-lived).
- [ ] P3.5 Add HPA:
  - primary metric session load ratio (`activeSessions / maxSessions`).
  - target value `50%`.
  - secondary metric CPU.
  - target value `60%`.
  - fast scale-up, conservative scale-down.
- [ ] P3.5.1 Verify scale-up threshold is below worker admission saturation threshold (LiveKit rule).
- [ ] P3.5.2 Configure stabilization windows:
  - scale-up: minimal.
  - scale-down: 10-15 minutes.
- [ ] P3.5.3 Set `PICOCLAW_LIVEKIT_MAX_SESSIONS=12` in Deployment env.
- [ ] P3.6 Add PDB to protect availability during node upgrades.
- [ ] P3.7 Add Secrets + ConfigMap strategy:
  - sensitive values in Kubernetes Secrets only.
  - non-sensitive runtime config in ConfigMap where applicable.
- [ ] P3.8 Add separate staging/prod LiveKit and Manager endpoints.
- [ ] P3.9 Set runtime values for this rollout:
  - `--agent-name cheeko-picoclaw`
  - LiveKit endpoint `ws://64.227.170.31:7880`
  - Manager API endpoint `http://64.227.170.31:8002/toy`

Acceptance:
- Rollout/rollback can be performed without global outage.
- Scale up/down does not create workspace corruption.
- HPA adds pods before existing pods hit session saturation.

## Phase 4: Verification, Load, and Chaos Testing
- [ ] P4.1 Concurrent reconnect storm test for same `device_mac`.
- [ ] P4.2 Pod kill during sync upload test.
- [ ] P4.3 Manager API transient failure and outbox replay test.
- [ ] P4.4 Rolling deployment with active sessions test.
- [ ] P4.5 Workspace restore latency test (fast path + background restore).
- [ ] P4.6 Config failure test matrix (missing file, malformed YAML, missing secret).

Acceptance:
- All failure modes end in deterministic behavior with no silent corruption.

## 7) Edge Cases to Explicitly Cover
- [ ] E1 Two pods receive same device simultaneously.
- [ ] E2 Lock holder crashes without release.
- [ ] E3 Lock heartbeat delayed by DB or network jitter.
- [ ] E4 Conflict loop on stale base revision.
- [ ] E5 Outbox replay includes stale payload after newer revision committed.
- [ ] E6 Restore payload includes deleted files and protected core files.
- [ ] E7 Binary/NUL content rejected safely.
- [ ] E8 Session ends while background restore still running.
- [ ] E9 Pod receives SIGTERM during workspace upload.
- [ ] E10 Manager API partial outage during startup bootstrap.
- [ ] E11 Mis-mounted `config.json`/`.security.yml` path mismatch.
- [ ] E12 Environment variable accidentally overrides intended secret.

## 8) Config and Security Loading Contract (Kubernetes)
- [ ] C1 Mount:
  - `/etc/picoclaw/config.json`
  - `/etc/picoclaw/.security.yml`
- [ ] C2 Start command always includes:
  - `--config /etc/picoclaw/config.json`
- [ ] C3 Validate at startup:
  - config exists and parses.
  - `.security.yml` exists/parses if strict mode enabled.
  - effective credentials present for selected providers.
- [ ] C4 Redacted source log:
  - `flag`, `env`, `security-file` precedence shown without secret values.

## 9) Task Breakdown by Repo

## `D:\cheeko-backend\main\manager-api-node`
- [ ] Add migration for `workspace_locks`.
- [ ] Add `lock.service.js`.
- [ ] Add lock endpoints + service-key auth.
- [ ] Refactor `workspace.service.js` write path to transactional revision-safe logic.
- [ ] Add integration tests for lock lease and concurrent sync writes.
- [ ] Add metrics/log events for lock contention and conflict frequency.

## `D:\picoclaw`
- [ ] Add manager lock client in `cmd/picoclaw-livekit`.
- [ ] Replace primary lock path in bridge lifecycle with DB lock acquire/release/heartbeat.
- [ ] Add strict config startup checks and clear error messages.
- [ ] Add drain-first shutdown path in worker lifecycle.
- [ ] Extend health/readiness responses with dependency state.
- [ ] Add tests for lock failure, reconnect handoff, and shutdown behavior.

## 10) Recommended Rollout Sequence
1. Manager lock + transactional sync in staging.
2. Agent DB lock integration in staging.
3. EKS deploy in staging with autoscaling disabled.
4. Load and chaos tests.
5. Enable HPA and rolling update policy.
6. Production canary (small traffic/device subset).
7. Full production rollout.

## 10.1) AWS CLI Execution Checklist (EKS)
1. Build and push image to ECR:
   - `aws ecr create-repository --repository-name picoclaw-livekit --region <region>`
   - `aws ecr get-login-password --region <region> | docker login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com`
   - `docker build -f tmp/Dockerfile.livekit.kind -t picoclaw-livekit:eks .`
   - `docker tag picoclaw-livekit:eks <account>.dkr.ecr.<region>.amazonaws.com/picoclaw-livekit:<tag>`
   - `docker push <account>.dkr.ecr.<region>.amazonaws.com/picoclaw-livekit:<tag>`
2. Create/prepare EKS cluster and nodegroup (if not already present), then configure kubeconfig:
   - `aws eks update-kubeconfig --name <cluster-name> --region <region>`
3. Apply namespace and secrets:
   - Create `picoclaw-config` secret from `config.json` and `.security.yml`.
   - Create/update `picoclaw-secrets` for `manager_api_url` and `manager_api_secret`.
4. Update Deployment image and agent name:
   - image: `<account>.dkr.ecr.<region>.amazonaws.com/picoclaw-livekit:<tag>`
   - args include `--agent-name cheeko-picoclaw`
5. Apply manifests:
   - `kubectl apply -f deploy/k8s/livekit-deployment.yaml`
   - `kubectl apply -f deploy/k8s/livekit-service.yaml`
   - `kubectl apply -f deploy/k8s/livekit-pdb.yaml`
   - `kubectl apply -f deploy/k8s/livekit-hpa.yaml`
6. Verify worker registration and room join:
   - `kubectl -n picoclaw-dev logs -f deployment/picoclaw-livekit`
   - confirm `agent=cheeko-picoclaw` and successful `Joined room`.

Notes for EKS:
- Keep `RollingUpdate` strategy in EKS deployment (do not use local kind `hostNetwork` pattern).
- Workspace durability remains in Manager API/DB; pod `/opt/picoclaw` stays ephemeral (`emptyDir`).

## 11) Definition of Done
- [ ] All phase acceptance criteria pass.
- [ ] Staging soak test (24-48h) has no unresolved lock/sync errors.
- [ ] Runbooks for incident response and rollback are published.
- [ ] Production canary is stable before 100% rollout.

## 12) Nice-to-Have Follow-ups
- [ ] Add Prometheus/Grafana dashboards for lock and sync lifecycle.
- [ ] Add per-device sync SLOs and alerting.
- [ ] Add workspace snapshot compaction/retention policy.
- [ ] Add automatic config schema validation CI gate for deploy artifacts.

## 13) Session-Load Metric Adapter Runbook (HPA Primary Metric)
1. Apply deployment primitives:
   - `kubectl apply -f deploy/k8s/livekit-deployment.yaml`
   - `kubectl apply -f deploy/k8s/livekit-service.yaml`
   - `kubectl apply -f deploy/k8s/livekit-pdb.yaml`
   - `kubectl apply -f deploy/k8s/livekit-podmonitor.yaml` (if Prometheus Operator is used)
2. Install or update Prometheus Adapter with custom rule file:
   - `helm upgrade --install prometheus-adapter prometheus-community/prometheus-adapter -n monitoring -f deploy/k8s/prometheus-adapter-values.yaml`
3. Verify custom metric is exposed:
   - `kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta1" | findstr picoclaw_livekit_session_load_percent`
   - `kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta1/namespaces/default/pods/*/picoclaw_livekit_session_load_percent"`
4. Apply dual-metric HPA:
   - `kubectl apply -f deploy/k8s/livekit-hpa.yaml`
5. Validate scaling behavior:
   - HPA should scale up when session-load average exceeds `50`.
   - CPU `60%` remains a secondary safety metric.
   - Worker `max_sessions=12` and load threshold remains above autoscale trigger.
