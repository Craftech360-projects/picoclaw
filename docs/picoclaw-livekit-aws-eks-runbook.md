# Picoclaw LiveKit on AWS EKS - Deployment and Operations Runbook

Last updated: 2026-05-20

## 1. Purpose

This document explains how Picoclaw LiveKit voice agent is deployed and operated on AWS EKS, including:

- Runtime architecture
- Workspace lifecycle (create/restore/sync)
- Config and secret management (`config.json` and `.security.yml`)
- Autoscaling with dual metrics (session load + CPU)
- Monitoring and troubleshooting commands

This is intended as the single operational runbook for production and pre-production.

## 2. Current Production Baseline

Cluster and namespace:

- EKS cluster: `arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks`
- Namespace: `picoclaw-dev`

LiveKit worker identity and endpoints:

- Agent name: `cheeko-picoclaw`
- LiveKit URL: `ws://64.227.170.31:7880`
- Manager API URL: `http://64.227.170.31:8002/toy`

Kubernetes manifests:

- Deployment: `deploy/k8s/livekit-deployment.yaml`
- Service: `deploy/k8s/livekit-service.yaml`
- HPA: `deploy/k8s/livekit-hpa.yaml`
- PDB: `deploy/k8s/livekit-pdb.yaml`
- Prometheus Adapter values: `deploy/k8s/prometheus-adapter-values.yaml`

## 3. Runtime Architecture

High-level flow:

1. MQTT Gateway receives device session start.
2. Gateway creates LiveKit room and dispatches `cheeko-picoclaw` worker.
3. Worker joins LiveKit room, opens STT stream, and publishes TTS track.
4. Worker restores workspace for device from Manager API.
5. Worker runs voice pipeline and periodically syncs workspace changes back to Manager API.

Core components:

- `mqtt-gateway` (external service): session orchestration and bridge
- `picoclaw-livekit` (EKS Deployment): worker process
- LiveKit server (`ws://64.227.170.31:7880`)
- Manager API (`/toy` base path)
- Prometheus + Prometheus Adapter + HPA

## 4. Workspace Lifecycle and Concurrency Model

### 4.1 Fresh workspace creation

Each pod has:

- workspace template path: `/opt/picoclaw/workspace-template`
- per-device active workspace path: `/opt/picoclaw/workspace-device-<identity>`

On session start, worker:

1. Hydrates default template for device session.
2. Validates active skills from room metadata.
3. Restores files from Manager API.

### 4.2 Restore behavior

Two restore patterns are used:

- Fast-path restore for quick startup
- Background full restore to converge final state

Log markers to confirm restore:

- `workspace fast-path restore completed`
- `workspace restore completed`
- `workspace background full restore completed`

### 4.3 Sync back to Manager

Workspace sync loop periodically uploads delta/full file state to Manager API.

Log markers:

- `workspace-sync uploaded to manager`
- `workspace_sync_saved_count`

### 4.4 Distributed lock for scale safety

For multi-pod safety, worker acquires lock per device/session before mutating workspace.

Lock behavior:

- Acquire distributed manager lock
- Acquire local per-device lock
- Release both on bridge/session close

This prevents concurrent workspace corruption when multiple pods race for same device.

## 5. `config.json` and `.security.yml` Management

### 5.1 Source of truth in Kubernetes

In EKS, runtime does not use host paths like:

- `C:\Users\rahul\.picoclaw\config.json`
- `C:\Users\rahul\.picoclaw\.security.yml`

Instead, both files are mounted from K8s secret `picoclaw-config`:

- `/etc/picoclaw/config.json`
- `/etc/picoclaw/.security.yml`

Deployment mounts are in `deploy/k8s/livekit-deployment.yaml`.

### 5.2 How to update credentials/settings

Edit source files (or generated temp files), then recreate secret and rollout.

Example:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev delete secret picoclaw-config
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev create secret generic picoclaw-config --from-file=config.json=<path-to-config.json> --from-file=.security.yml=<path-to-security.yml>
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev rollout restart deployment/picoclaw-livekit
```

### 5.3 Updating `livekit_service` in `.security.yml`

If `livekit_service` keys/url change:

1. Update `.security.yml`
2. Recreate `picoclaw-config` secret
3. Restart deployment
4. Verify logs show new `server_url` and successful worker registration

## 6. Deployment Configuration (EKS)

Current worker sizing (per pod):

- Requests: `1 vCPU`, `2Gi` memory, `5Gi` ephemeral
- Limits: `2 vCPU`, `4Gi` memory, `10Gi` ephemeral
- Max sessions per pod: `PICOCLAW_LIVEKIT_MAX_SESSIONS=12`

Health endpoints:

- `/health` on port `8192`
- `/ready` on port `8192`

Graceful drain:

- `terminationGracePeriodSeconds: 900`
- preStop sleep: `10s`

## 7. Autoscaling Design

## 7.1 Dual metrics in HPA

HPA file: `deploy/k8s/livekit-hpa.yaml`

Metrics:

1. Session load metric: `picoclaw_livekit_session_load_percent` target `50`
2. CPU utilization target `50%`

Replica policy:

- `minReplicas: 1`
- `maxReplicas: 3`

Behavior:

- Scale up quickly: `100%` every `30s`, no stabilization window
- Scale down conservatively: `10%` every `60s`, stabilization `900s`

This follows LiveKit guidance to scale up below saturation (worker saturation often near 70-75%).

## 7.2 Session load math

Worker exports:

- `picoclaw_livekit_max_sessions`
- `picoclaw_livekit_session_load_percent = active_sessions / max_sessions * 100`

With `max_sessions=12`:

- 1 active session ~ 8.33%
- 6 active sessions ~ 50% (scale trigger threshold)
- 9 active sessions ~ 75%
- 12 active sessions ~ 100%

## 7.3 Why Prometheus Adapter is required

K8s HPA can use CPU directly (via metrics-server), but custom app metrics require:

- Prometheus to scrape/store metric
- Prometheus Adapter to expose metric via `custom.metrics.k8s.io`

Without adapter, HPA reports metric as unknown and custom metric scaling is disabled.

## 8. Monitoring Stack for Autoscaling

Monitoring namespace: `monitoring`

Installed components:

- `prometheus-server`
- `prometheus-adapter`
- `metrics-server` (EKS addon in kube-system)

Adapter mapping config:

- `deploy/k8s/prometheus-adapter-values.yaml`
- Maps `picoclaw_livekit_session_load_percent` to pod custom metric

## 9. Operations Commands

### 9.1 Agent pod logs

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev logs -f deployment/picoclaw-livekit
```

### 9.2 List agent pods

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev get pods -l app=picoclaw-livekit
```

### 9.3 Check HPA state

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev get hpa picoclaw-livekit -o wide
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev describe hpa picoclaw-livekit
```

### 9.4 Check custom session metric directly

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks get --raw "/apis/custom.metrics.k8s.io/v1beta1/namespaces/picoclaw-dev/pods/*/picoclaw_livekit_session_load_percent?labelSelector=app%3Dpicoclaw-livekit"
```

### 9.5 Check pod CPU/memory metrics

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev top pods
```

### 9.6 Rollout deployment update

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev apply -f deploy/k8s/livekit-deployment.yaml
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev rollout status deployment/picoclaw-livekit
```

## 10. Troubleshooting Guide

### 10.1 Symptom: `AGENT-TIMEOUT` but agent actually joined

What to check:

- Gateway code version includes agent join hardening for `cheeko-picoclaw`
- Gateway process restarted after patch
- Agent event logs (`agent_state_changed`, `speech_created`) are present

Likely cause:

- Stale gateway process with old join detection logic

### 10.2 Symptom: HPA shows `<unknown>` metrics

What to check:

- `kubectl get apiservice | findstr custom.metrics.k8s.io`
- `kubectl -n monitoring get pods`
- adapter logs
- `kubectl top pods` works for CPU path

Likely cause:

- Missing metrics-server or adapter
- Adapter cannot reach Prometheus service/port

### 10.3 Symptom: workspace lock endpoint 404

Log example:

- `/toy/agent/device/.../workspace-lock/acquire` returns 404

Likely cause:

- Manager API deployment does not include workspace lock routes
- Wrong `MANAGER_API_URL` base path

### 10.4 Symptom: room join timeout with local cluster networking

Likely cause:

- ICE/network path issues between pod/container and LiveKit
- Local Docker/kind network constraints

Fix direction:

- Validate LiveKit reachability from pod
- Re-test on EKS/publicly routable network

### 10.5 Symptom: duplicated/garbled emoji in logs (`✅` etc.)

Cause:

- Terminal encoding mismatch

Fix:

- Use UTF-8 terminal locale/encoding

## 11. Capacity and Scaling Notes

Current capacity (initial):

- 1 to 3 pods
- Up to ~12 sessions per pod configured
- Estimated practical range per pod depends on STT/TTS/model profile and latency budget

Recommended process:

1. Run controlled concurrency tests.
2. Observe turn latency and CPU/memory/session-load curves.
3. Tune `PICOCLAW_LIVEKIT_MAX_SESSIONS`, HPA thresholds, and pod resources.

## 12. Security and Change Control

- Keep sensitive values only in secrets.
- Do not commit production secrets to git.
- Rotate LiveKit and Manager credentials by updating secret and rolling deployment.
- Keep `.security.yml` changes auditable with commit history and release notes.

## 13. File Index

- `deploy/k8s/livekit-deployment.yaml`
- `deploy/k8s/livekit-hpa.yaml`
- `deploy/k8s/livekit-service.yaml`
- `deploy/k8s/livekit-pdb.yaml`
- `deploy/k8s/livekit-podmonitor.yaml`
- `deploy/k8s/prometheus-adapter-values.yaml`

## 14. Quick Health Checklist

1. Agent pod `Running` and `Ready=1/1`.
2. Worker logs show LiveKit `Connected` and `Worker registered`.
3. Session logs show room join and STT/TTS events.
4. Workspace restore/sync logs are present.
5. HPA shows both metrics: session load and CPU.
6. Custom metric query returns items for LiveKit pod(s).

