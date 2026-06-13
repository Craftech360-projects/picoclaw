# LiveKit Agent Capacity and Hardening Notes

Last updated: 2026-06-12

## Current production shape

- LiveKit server: LiveKit Cloud (`wss://cheeko-prod-68ib8ma4.livekit.cloud`)
- EKS cluster: `picoclaw-eks` in `ap-south-2`
- Namespace: `picoclaw-dev`
- Workload: `Deployment/picoclaw-livekit`
- Deployment replicas: `2`
- HPA: `minReplicas=2`, `maxReplicas=10`
- Node group: `picoclaw-ng-c6a-large`
- Node group scaling: `minSize=2`, `desiredSize=2`, `maxSize=5`
- Node autoscaler: Cluster Autoscaler with ASG tag discovery
- EC2 On-Demand Standard vCPU quota: `64`

## Capacity interpretation

The agent pod is configured with:

- `PICOCLAW_LIVEKIT_MAX_SESSIONS=12`
- HPA session-load target: `70`
- CPU target: `50%`

This means:

- 1 active session on one pod reports about `8.3%` load.
- 6 active sessions on one pod reports about `50%` load.
- 8-9 active sessions on one pod reports about `67-75%` load and should trigger scale-up pressure.
- 12 active sessions on one pod is the configured per-pod ceiling, not a comfort target.
- 2 warm pods give a configured ceiling of about 24 concurrent sessions before HPA adds more pods.
- 10 pods give a configured ceiling of about 120 concurrent sessions, subject to real latency and provider limits.

For billing and sizing, use peak concurrent voice sessions and active minutes, not total registered users. If there are 100 total users but only 5-15 are active at the same time, the current 2-pod baseline should usually be enough from the Kubernetes side. If 50-100 users can talk at once, the HPA and node group must scale up and provider/API limits must be tested separately.

## Current AWS cost baseline

Current baseline cost is mostly fixed by keeping two `c6a.large` nodes warm:

- `c6a.large` is roughly half the size of the previously tested `c6a.xlarge`.
- Two warm nodes should be much cheaper than the previous two-`c7i.xlarge` baseline; verify exact pricing with AWS Pricing or Cost Explorer before budgeting.
- EKS control plane: about `$73/month`.
- EBS/root volume storage and small extras: roughly `$25-55/month`, depending on actual volume sizes.

Expected current AWS baseline is now mainly the EKS control plane plus two small compute nodes, excluding LiveKit Cloud, LLM, STT, TTS, database, and manager API costs.

Temporary scale-out cost:

- Each extra `c6a.large` adds one small EKS worker node plus root volume storage while the instance exists.
- Rolling updates can briefly add capacity because each agent pod requests `750m` CPU, `512Mi` memory, and `10Gi` ephemeral storage.
- The `900s` termination grace period protects active voice sessions, but it can also keep old pods reserving node resources during rollout while new pods surge.
- Cluster Autoscaler should remove empty/unneeded nodes after its scale-down cooldown.

## C6A capacity-test note

The previous capacity-test node group has been promoted to production:

- Node group: `picoclaw-ng-c6a-large`
- Instance type: `c6a.large`
- Scaling: `minSize=2`, `desiredSize=2`, `maxSize=5`
- Production Deployment: `picoclaw-livekit`
- Production agent name: `cheeko-agent1`

The 2026-06-12 real-audio canary test on one `c6a.large` pod passed `12`, `14`, and `15` rooms from the LiveKit/VAD/STT/LLM path with low memory usage and no pod restarts. At `16` rooms, all rooms joined and VAD/STT started, but only `13/16` reached LLM before the short test window ended. The safe production setting is therefore `PICOCLAW_LIVEKIT_MAX_SESSIONS=12`.

ElevenLabs is still the external response-audio gate. Production smoke on 2026-06-12 reached LiveKit dispatch, room join, VAD, STT, and LLM, but ElevenLabs returned `payment_issue` for TTS bytes. Re-test full response audio after the ElevenLabs account/plan is fixed.

## Hardening already applied

- Deployment rollout strategy uses `maxSurge=1`, `maxUnavailable=0`, and `minReadySeconds=10`.
- PDB uses `maxUnavailable=1`, so voluntary disruptions can proceed once two replicas are available.
- Workload does not mount a Kubernetes service account token.
- Pod runs as numeric non-root UID/GID `10001`.
- `fsGroup=10001` makes the writable `emptyDir` workspace usable by the non-root process.
- Container drops all Linux capabilities.
- `allowPrivilegeEscalation=false`.
- `seccompProfile=RuntimeDefault`.
- Root filesystem is read-only.
- Writable runtime paths are explicit `emptyDir` mounts: `/opt/picoclaw` and `/tmp`.
- ECR repository uses immutable tags and scan-on-push.
- Deployment image is pinned by digest.

## NetworkPolicy status

`deploy/k8s/network-policy/livekit-networkpolicy.yaml` is validated but not applied.

Reason: AWS VPC CNI network policy enforcement is currently disabled. Applying the policy while the CNI ignores it gives a false sense of security; enabling enforcement without a controlled rollout can break DNS, provider API egress, or metrics scraping.

Safe order:

1. Enable an EKS-supported NetworkPolicy engine for the cluster, preferably by managing/configuring the `vpc-cni` addon.
2. Confirm `aws-node` is running with network policy enabled.
3. Server-dry-run the policy.
4. Apply during a maintenance window.
5. Verify DNS, LiveKit Cloud websocket, Manager API, STT/TTS/LLM providers, Postgres, and Prometheus scrape path.

Rollback:

```powershell
kubectl -n picoclaw-dev delete networkpolicy picoclaw-livekit-egress
```

## Validation commands

```powershell
kubectl apply --dry-run=server -f deploy/k8s/livekit-deployment.yaml
kubectl apply --dry-run=server -f deploy/k8s/livekit-hpa.yaml
kubectl apply --dry-run=server -f deploy/k8s/livekit-pdb.yaml
kubectl apply --dry-run=server -f deploy/k8s/cluster-autoscaler/cluster-autoscaler.yaml
kubectl apply --dry-run=server -f deploy/k8s/network-policy/livekit-networkpolicy.yaml

kubectl -n picoclaw-dev rollout status deployment/picoclaw-livekit
kubectl -n picoclaw-dev get deploy,hpa,pdb
kubectl -n picoclaw-dev get pods -l app=picoclaw-livekit -o wide
kubectl get nodes
```

Rollback deployment hardening:

```powershell
kubectl -n picoclaw-dev rollout undo deployment/picoclaw-livekit
```
