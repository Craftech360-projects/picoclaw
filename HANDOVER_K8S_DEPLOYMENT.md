# Picoclaw LiveKit Kubernetes Deployment Handover

> Superseded note, 2026-06-10: the live EKS deployment has moved from the one-node `m7i.xlarge` test to `picoclaw-ng-c7i-xlarge` with Cluster Autoscaler, HPA `2-10`, hardened non-root pods, and staged NetworkPolicy. Use `docs/picoclaw-livekit-aws-eks-runbook.md` and `deploy/k8s/capacity-and-hardening.md` as the current source of truth.

Date: 2026-05-29
Repo: D:\picoclaw
Branch at handover: codex/voice-only-livekit
Commit at handover: ed57acf0e75904a1df57e2079a15f2914aeba2e0

## 1) Goal

Deploy and operate `picoclaw-livekit` on AWS EKS with:

- Agent name: `cheeko-agent1`
- LiveKit server: `wss://cheeko-prod-68ib8ma4.livekit.cloud`
- Manager API base URL: `http://139.59.7.72:8002/toy`
- Workspace restore/sync + lock flow enabled
- HPA dual metrics: session load + CPU, capped at one replica for the current `m7i.xlarge` test

## 2) Key Manifest Files

- `deploy/k8s/livekit-deployment.yaml`
- `deploy/k8s/livekit-service.yaml`
- `deploy/k8s/livekit-pdb.yaml`
- `deploy/k8s/livekit-hpa.yaml`
- `deploy/k8s/prometheus-adapter-values.yaml`
- `deploy/k8s/livekit-podmonitor.yaml` (optional if Prometheus Operator is used)

## 3) Cluster Context

Expected EKS context:

`arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks`

Namespace used:

`picoclaw-dev`

Create namespace if missing:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks create ns picoclaw-dev --dry-run=client -o yaml | kubectl apply -f -
```

## 4) Required Runtime Secrets

Two K8s secrets are required:

### 4.1 `picoclaw-config`

Contains mounted files:

- `config.json` -> `/etc/picoclaw/config.json`
- `.security.yml` -> `/etc/picoclaw/.security.yml`

Create/update:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev delete secret picoclaw-config --ignore-not-found
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev create secret generic picoclaw-config --from-file=config.json=<PATH_TO_CONFIG_JSON> --from-file=.security.yml=<PATH_TO_SECURITY_YML>
```

### 4.2 `picoclaw-secrets`

Required keys used by deployment env:

- `manager_api_url`
- `manager_api_service_key`
- `stt_database_url`
- `direct_url`

Create/update:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev delete secret picoclaw-secrets --ignore-not-found
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev create secret generic picoclaw-secrets \
  --from-literal=manager_api_url=http://139.59.7.72:8002/toy \
  --from-literal=manager_api_service_key=<VALUE> \
  --from-literal=stt_database_url=<VALUE> \
  --from-literal=direct_url=<VALUE>
```

## 5) Deploy Application

Apply manifests:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev apply -f deploy/k8s/livekit-deployment.yaml
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev apply -f deploy/k8s/livekit-service.yaml
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev apply -f deploy/k8s/livekit-pdb.yaml
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev apply -f deploy/k8s/livekit-hpa.yaml
```

Rollout check:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev rollout status deployment/picoclaw-livekit
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev get pods -l app=picoclaw-livekit
```

## 6) Monitoring Stack for Dual-Metric HPA

Dual metric requires:

1. `metrics-server` addon (for CPU)
2. Prometheus server (scrapes app metrics)
3. Prometheus Adapter (`custom.metrics.k8s.io`)

### 6.1 Install metrics-server addon

```powershell
aws eks create-addon --cluster-name picoclaw-eks --region ap-south-2 --addon-name metrics-server
```

### 6.2 Install Prometheus + Adapter (helm)

If helm is unavailable, use local binary in `D:\picoclaw\bin\helm.exe`.

```powershell
D:\picoclaw\bin\helm.exe repo add prometheus-community https://prometheus-community.github.io/helm-charts
D:\picoclaw\bin\helm.exe repo update
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks create ns monitoring --dry-run=client -o yaml | kubectl apply -f -

D:\picoclaw\bin\helm.exe upgrade --install prometheus prometheus-community/prometheus --namespace monitoring --kube-context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks --set alertmanager.enabled=false --set kube-state-metrics.enabled=false --set prometheus-node-exporter.enabled=false --set prometheus-pushgateway.enabled=false --set server.persistentVolume.enabled=false

D:\picoclaw\bin\helm.exe upgrade --install prometheus-adapter prometheus-community/prometheus-adapter --namespace monitoring --kube-context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -f deploy/k8s/prometheus-adapter-values.yaml
```

## 7) Verify Autoscaling Metrics

### 7.1 CPU path

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev top pods
```

### 7.2 Custom metrics API path

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks get apiservice | findstr custom.metrics.k8s.io
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks get --raw "/apis/custom.metrics.k8s.io/v1beta1"
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks get --raw "/apis/custom.metrics.k8s.io/v1beta1/namespaces/picoclaw-dev/pods/*/picoclaw_livekit_session_load_percent?labelSelector=app%3Dpicoclaw-livekit"
```

### 7.3 HPA result

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev get hpa picoclaw-livekit -o wide
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev describe hpa picoclaw-livekit
```

Expected target style:

- `picoclaw_livekit_session_load_percent`: `X/50`
- `cpu`: `Y%/50%`

## 7.4 Current Single `m7i.xlarge` Test Sizing

Production `deploy/k8s/livekit-deployment.yaml` is now tuned for a single `m7i.xlarge` managed node group:

- Node group: `picoclaw-ng-m7i-xlarge`
- Node type: `m7i.xlarge` (`4 vCPU`, `16Gi` memory)
- Node group size: `min=1`, `desired=1`, `max=1`
- Old node group: `picoclaw-ng-1` (`t3.xlarge`) deleted after successful migration
- Per-pod requests: `3 vCPU`, `6Gi` memory, `10Gi` ephemeral storage
- Per-pod limits: `4 vCPU`, `8Gi` memory, `20Gi` ephemeral storage
- Max sessions per pod: `PICOCLAW_LIVEKIT_MAX_SESSIONS=12`
- HPA range: `minReplicas=1`, `maxReplicas=1`
- Node selector: `node.kubernetes.io/instance-type=m7i.xlarge`

Node autoscaling is intentionally disabled for this test. Do not raise HPA above one replica until a second node or Karpenter/Cluster Autoscaler is added.

Capacity interpretation:

- 6 concurrent sessions = 50% session-load observation target
- 10-12 concurrent sessions = load-test range, not guaranteed production comfort
- 20+ concurrent sessions = add horizontal scaling first

## 8) Logs and Runtime Checks

Agent logs:

```powershell
kubectl --context arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks -n picoclaw-dev logs -f deployment/picoclaw-livekit
```

Look for:

- worker registered
- job assignment received
- joined room
- workspace restore and sync logs

## 9) Known Issues and Fixes

### 9.1 `AGENT-TIMEOUT` while agent is actually in room

Cause was gateway-side join/timer logic mismatch. Ensure latest gateway patch is deployed and process restarted.

### 9.2 HPA shows `<unknown>`

Usually missing metrics pipeline:

- CPU unknown -> metrics-server not active
- custom metric unknown -> adapter missing, wrong Prometheus URL/port, or no scrape annotations

### 9.3 EKS console says "current IAM principal doesn't have access"

Cluster auth mode is `CONFIG_MAP`, so map IAM user/role in `kube-system/aws-auth`.

## 10) Minimal Copy-Paste for New Chat

Use this prompt in next chat:

"Continue EKS deployment from D:\\picoclaw using deploy/k8s manifests. Verify picoclaw-livekit deployment, service, hpa dual metrics, and custom metric picoclaw_livekit_session_load_percent in cluster arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks namespace picoclaw-dev. If any part is missing, apply/fix and provide final verification outputs."

## 11) Optional Cleanup

Local k8s kind cluster was removed previously. AWS EKS remains active.

