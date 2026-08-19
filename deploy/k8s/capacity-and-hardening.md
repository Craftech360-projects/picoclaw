# LiveKit Agent Capacity and Hardening Notes

Last updated: 2026-08-19

## Real-device test: 50 physical toys (2026-08-19)

The only test on this page run with actual hardware. 50 paired toys were powered on
in stages against production, with the agent fleet left at its normal
`minReplicas=2` so Kubernetes autoscaling was exercised rather than bypassed.

| Toys on | Gateway RSS (4 shards) | Worst shard loop lag | Agent pods | Nodes | Timeouts |
|---|---|---|---|---|---|
| 0 (baseline) | 888 MB | 12.2 ms | 2 | 2 | - |
| 10 | 888 MB | 11.0 ms | 2 | 2 | 0 |
| 30 | 1127 MB | 11.9 ms | 4 | 3 | **2** |
| 50 | 1305 MB | 13.2 ms | 5 | 3 | 0 |

Findings:

- **Real toys cost less than synthetic load, not more.** 50 real toys used 1305 MB;
  150 synthetic sessions used 1326 MB. Children speak in bursts with pauses, while
  the load client streams continuously. The 150 figure is therefore conservative
  for real traffic. Note this measured 50 toys *connected and conversing*, not 50
  children talking simultaneously and continuously.
- **Event-loop lag never left the ~10 ms measurement floor** at any step. The
  gateway is not the constraint anywhere near this load.
- **Sharding routes real hardware correctly.** All devices landed on the shard
  their MAC hashes to, split 2/2/3/3 across four instances. Production MACs share
  an OUI prefix (`68:EE:8F:60:xx:xx`) and are near-sequential; FNV-1a spreads a
  50-device sequential batch 12/12/13/13, so batch allocation does not clump.
- **The autoscaling chain works on real load:** HPA 2 -> 4 -> 5 pods, Cluster
  Autoscaler 2 -> 3 nodes, unattended.
- **The 2 timeouts at 30 toys are the reactive-scaling gap, and are the one real
  defect this test found.** 30 toys briefly exceeded the 30 warm slots while HPA
  was still moving 2 -> 4 pods; two sessions waited past the 25 s agent-timeout and
  those children heard "server is busy". By 50 toys the fleet was already ahead and
  timeouts returned to zero. Pre-warming to 3-4 pods before the known evening peak
  removes this; reactive scaling alone cannot, because pod start (~24 s) plus node
  start (minutes) is slower than a burst of toys switching on.

## Measured end-to-end capacity, synthetic (2026-08-19)

Load-tested against production driving synthetic devices from the dev box. Numbers
below are measured, not derived. Prefer the real-device table above where they
overlap.

| Component | Ceiling | Basis |
|---|---|---|
| mqtt-gateway (4 sharded instances) | **150+ concurrent real-audio sessions** | full 60/90/120/150 ramp, zero restarts, zero agent-timeouts, 1326 MB total RSS |
| mqtt-gateway (single instance) | **~30-40** | 60 sessions with live agents drove RSS 172 MB -> 1.9 GB climbing and loop lag to 148 ms; aborted before the memory cap |
| agent fleet, warm | 30 | `MAX_SESSIONS=15` x `minReplicas=2` |
| agent fleet, max | ~135 in practice | see node-group note below |

Two corrections to earlier assumptions:

- **Silent sessions cost ~4-5x less than real ones.** Earlier gateway estimates
  (~150 for a single instance) were extrapolated from load clients that held
  connections without audio. With live agent audio a session costs ~28 MB and
  ~4.5% CPU, not ~6 MB and ~1.3%. Never certify capacity without agents serving.
- **`maxReplicas=10` does not yield 10 pods.** At `maxSize=5` nodes the 10th pod
  stays `Pending` on insufficient CPU, so the real fleet maximum is 9 pods / 135
  sessions. Raise the node group before relying on 150.

## Gateway horizontal scaling

The gateway was the binding constraint for the whole system until 2026-08-19: a
single Node process, one event loop, no ability to scale, and a SPOF for every
toy. It now runs **4 sharded instances** (`gw-0`..`gw-3`) on one host.

- A device is owned wholly by the instance its MAC hashes to (FNV-1a). No shared
  state, no load balancer, no sticky sessions.
- UDP needs no routing layer: the device learns its audio port from the hello
  response, which carries the owning instance's own port.
- manager-api runs the identical hash to route its settings-push to the owning
  instance's internal port. The two implementations must agree exactly;
  `tests/shard-contract.test.js` in cheeko-backend pins that.
- Ports per instance: UDP `8884-8887`, health `8004-8007`, internal `8091-8094`.

Sharding also removed a dispatch bottleneck: at 60 sessions a single instance hit
the 25 s agent-timeout on 28 of 60 sessions *despite free agent slots*, because one
event loop could not absorb ~1 arrival/sec while relaying audio. Four loops show
zero timeouts through 150.

**Open design question.** `docs/plans/2026-03-07-mqtt-gateway-scaling-design.md`
(approved) specifies EMQX shared subscriptions (`$share/gateway/device-server`)
rather than MAC-hash sharding. That plan predates a topology change: the gateway
no longer subscribes to `device-server` but to `internal/server-ingest`, fed by an
EMQX republish rule. Naive `$share` would round-robin a single device's messages
across instances and break its session; `hash_clientid` cannot fix it because the
publisher on that topic is the rule engine, not the device. MAC-hash was chosen
for the current topology. The cost is 4x MQTT fan-out (every instance receives
every message, three discard). The synthesis — a shard-aware republish rule
emitting `internal/server-ingest/0..3` — gives 1x delivery *and* per-device
affinity, and is the recommended optimization when fan-out actually bites.

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

- `PICOCLAW_LIVEKIT_MAX_SESSIONS=15`
- HPA session-load target: `60`
- CPU target: `50%`

This means:

- 1 active session on one pod reports about `6.7%` load.
- 8 active sessions on one pod reports about `53.3%` load.
- 9 active sessions on one pod reports `60%` load and should trigger scale-up pressure.
- 15 active sessions on one pod is the configured per-pod ceiling, not a comfort target.
- 2 warm pods give a configured ceiling of about 30 concurrent sessions before HPA adds more pods.
- 10 pods give a configured ceiling of about 150 concurrent sessions, subject to real latency and provider limits. **In practice only 9 pods schedule** at `maxSize=5` nodes — the 10th stays `Pending` on insufficient CPU.

Warm capacity is what bites under bursts, not the maximum. Scale-up is reactive:
the HPA triggers at 60% session load, a pod is Ready in ~24s, and a new node takes
minutes. Scale-down is far slower still — a 5-minute HPA stabilization window plus
`terminationGracePeriodSeconds: 900` per pod, so a scale-down from 10 to 2 takes
15-25 minutes to fully settle including node removal. A burst (post-outage
reconnects, or the evening peak) can outrun scale-up while sessions queue at the
gateway. Pre-warming for the known peak beats relying on reactive scaling.

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
- Production agent name: `cheeko-agent`

  Verify rather than trust this line — it said `cheeko-agent1` until 2026-08-17 and
  cost two people time, because a load test against the wrong name creates rooms
  no worker ever joins and reads as total failure:

  ```bash
  kubectl -n picoclaw-dev logs -l app=picoclaw-livekit --tail=200 | grep -oE "agent_name=[a-z0-9-]+" | sort -u
  ```

The 2026-06-13 real-audio canary test on one isolated `c6a.large` pod passed `18` rooms cleanly through dispatch, join, STT, VAD, LLM, cleanup, and quality summaries with low memory usage and no pod restarts. At `19` rooms, all rooms joined, but only `18` reached STT/VAD and `17` reached LLM/quality summaries. The balanced production setting is therefore `PICOCLAW_LIVEKIT_MAX_SESSIONS=15` with the HPA session-load target set to `60`.

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
