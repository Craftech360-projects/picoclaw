# LiveKit Voice Agent Load Testing Plan

Last updated: 2026-06-11

This plan validates whether the Picoclaw LiveKit voice agent can handle real concurrent usage on AWS EKS, not only a single smoke-test session. The capacity test target is the cost-optimized `c6a.xlarge` setup, not the existing `c7i.xlarge` pool.

Primary source: LiveKit field guide, "How to load test voice agents and get meaningful results" (`https://livekit.com/field-guides/guide/load-testing-voice-agents`).

## Goal

Validate production readiness for the `cheeko-agent1` LiveKit voice agent by measuring:

- concurrent LiveKit sessions
- agent join delay
- first greeting delivery
- user-perceived response latency
- STT, LLM, and TTS provider behavior
- timeout and disconnect behavior
- Kubernetes HPA and Cluster Autoscaler behavior
- pod CPU, memory, restart, and scheduling stability
- failure rate under gradually increasing load

The goal is not to instantly prove "100 users works." The goal is to find the highest safe concurrency level with acceptable voice latency and low error rate.

Capacity discovery goals:

- host a canary agent on a new `c6a.xlarge` node group
- find the highest safe sessions per pod on `c6a.xlarge`
- decide whether the Cerebrium-style starting concurrency of `4` can be raised to `8`, `12`, `18`, or higher
- choose the final `PICOCLAW_LIVEKIT_MAX_SESSIONS`, HPA target, HPA max replicas, and node group shape from measurements instead of assumptions

## Current Deployment Baseline

| Item | Value |
| --- | --- |
| LiveKit server | LiveKit Cloud |
| Agent name | `cheeko-agent1` |
| EKS cluster | `picoclaw-eks` |
| AWS region | `ap-south-2` |
| Namespace | `picoclaw-dev` |
| Deployment | `picoclaw-livekit` |
| HPA | `minReplicas=2`, `maxReplicas=10` |
| Node group | `picoclaw-ng-c7i-xlarge` |
| Node group size | `minSize=2`, `desiredSize=2`, `maxSize=10` |
| Node autoscaler | Cluster Autoscaler |
| Pod max sessions | `PICOCLAW_LIVEKIT_MAX_SESSIONS=12` |
| HPA session target | `picoclaw_livekit_session_load_percent = 50` |
| CPU target | `50%` |
| NetworkPolicy | staged, not enforced yet |

Proposed cost-optimized target to test:

| Item | Value |
| --- | --- |
| Candidate node group | `picoclaw-ng-c6a-xlarge` |
| Candidate instance | `c6a.xlarge` |
| Candidate node size | `4 vCPU`, `8 GiB` |
| Candidate price | about `$0.0935/hour`, about `$2.24/day` per node |
| Candidate node scaling | `minSize=1`, `desiredSize=1`, `maxSize=8` |
| Candidate HPA | `minReplicas=1`, `maxReplicas=8` |
| Candidate start concurrency | `PICOCLAW_LIVEKIT_MAX_SESSIONS=4` |
| Candidate graceful drain | `900s` |

Capacity interpretation:

- 1 active session on one pod reports about `8.3%` load.
- 6 active sessions on one pod reports about `50%` load and should create HPA scale-up pressure.
- 12 sessions on one pod is the configured ceiling, not the comfort target.
- 2 warm pods provide about 24 configured session slots before extra scale-out.
- 10 pods provide about 120 configured session slots, subject to real latency and external provider limits.
- A realistic comfort target is likely lower than the configured ceiling. Treat `60` concurrent sessions as an important production-readiness target and `100` concurrent sessions as a stress test.

## Key Principle

Do not burst from `0` to target concurrency.

LiveKit's guidance is to ramp gradually. Burst tests create artificial failures because rooms, agent dispatch, LiveKit capacity, provider quotas, and Kubernetes node scale-out are all forced to happen at the same instant. That mostly tests cold-start shock, not normal production traffic.

Use a controlled ramp:

- start small
- hold each level long enough to observe stability
- increase gradually
- stop when latency/errors become unacceptable
- hold the final target for at least 5-10 minutes

## Tool Choice

Use LiveKit CLI first:

```powershell
lk perf agent-load-test `
  --rooms 5 `
  --agent-name cheeko-agent1 `
  --echo-speech-delay 10s `
  --duration 5m
```

What this does:

- creates one LiveKit room per simulated session
- dispatches the configured agent
- joins an echo participant
- waits for the agent to speak
- echoes the agent's audio back after `--echo-speech-delay`
- prints load-test statistics when done

Important limitation:

The CLI echo test may not send the exact same room metadata, device metadata, active skills, or gateway control events as the real product flow. If the agent requires gateway-specific metadata to fully exercise workspace restore, device identity, or skill selection, use the CLI for initial agent-dispatch testing, then build a custom load runner that drives the real gateway path or creates rooms with matching metadata through the LiveKit Server SDK.

Do not use `lk load-test` for agent behavior. That command is for WebRTC transport load. Use `lk perf agent-load-test` for agent-specific load testing.

## Preflight Checklist

Run these before every test window.

Set variables:

```powershell
$Context = "arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks"
$Namespace = "picoclaw-dev"
```

Check workload health:

```powershell
kubectl --context $Context -n $Namespace get deploy,hpa,pdb,svc
kubectl --context $Context -n $Namespace get pods -l app=picoclaw-livekit -o wide
kubectl --context $Context -n $Namespace rollout status deployment/picoclaw-livekit
kubectl --context $Context -n $Namespace top pods
```

Check HPA and custom metrics:

```powershell
kubectl --context $Context -n $Namespace get hpa picoclaw-livekit -o wide
kubectl --context $Context -n $Namespace describe hpa picoclaw-livekit
kubectl --context $Context get --raw "/apis/custom.metrics.k8s.io/v1beta1/namespaces/picoclaw-dev/pods/*/picoclaw_livekit_session_load_percent?labelSelector=app%3Dpicoclaw-livekit"
```

Check Cluster Autoscaler:

```powershell
kubectl --context $Context -n kube-system get deploy,pod -l app.kubernetes.io/name=cluster-autoscaler
kubectl --context $Context -n kube-system logs deployment/cluster-autoscaler --tail=100
```

Check node group scaling:

```powershell
aws eks describe-nodegroup `
  --region ap-south-2 `
  --cluster-name picoclaw-eks `
  --nodegroup-name picoclaw-ng-c7i-xlarge `
  --query "nodegroup.scalingConfig"
```

For the `c6a.xlarge` canary test, check the candidate node group after creating it:

```powershell
aws eks describe-nodegroup `
  --region ap-south-2 `
  --cluster-name picoclaw-eks `
  --nodegroup-name picoclaw-ng-c6a-xlarge `
  --query "nodegroup.scalingConfig"
```

Check EC2 quota:

```powershell
aws service-quotas get-service-quota `
  --region ap-south-2 `
  --service-code ec2 `
  --quota-code L-1216C47A
```

Confirm externally:

- LiveKit Cloud plan supports the target concurrent sessions.
- STT provider quota supports the target concurrent streams.
- LLM provider quota supports target request rate and token throughput.
- TTS provider quota supports target concurrent synthesis.
- Load testing traffic cost is approved.
- Agent speaks first. The LiveKit echo tester only responds after it hears the agent.
- Current deployed image digest is the intended release.

## Load Generator Environment

Small tests can run from a developer laptop. For `25+` concurrent rooms, prefer a cloud VM to avoid local ISP, NAT, and laptop CPU limits.

Recommended VM location:

- same broad region as LiveKit project users, or close to `ap-south-2` for India-focused testing
- enough CPU and network bandwidth
- stable outbound internet

Install LiveKit CLI:

```bash
curl -sSL https://get.livekit.io/cli | bash
```

Or on Windows:

```powershell
winget install LiveKit.LiveKitCLI
```

Authenticate or configure LiveKit project:

```bash
lk cloud auth
lk project list
lk project set-default <project-name>
```

If not using a default project, pass URL/API credentials explicitly or set environment variables according to LiveKit CLI precedence.

Linux VM tuning before larger tests:

```bash
ulimit -n 65535
sudo sysctl -w fs.file-max=2097152
sudo sysctl -w net.core.somaxconn=65535
sudo sysctl -w net.core.rmem_max=25165824
sudo sysctl -w net.core.wmem_max=25165824
```

For hundreds of concurrent sessions, use multiple VMs rather than one overloaded generator.

## Per-Pod Capacity Calibration

Run this before deciding whether `PICOCLAW_LIVEKIT_MAX_SESSIONS=12` is too low.

The current `8.3%` load for one active session is a slot calculation, not CPU:

```text
1 active session / 12 configured max sessions * 100 = 8.3%
```

Because the worker is written in Go, CPU and memory may allow more than 12 sessions per pod. But voice capacity is not only Go runtime cost. Each session also consumes WebRTC audio handling, VAD, streaming STT, LLM streaming, TTS synthesis, workspace state, network I/O, and provider quota. This calibration test finds the real per-pod comfort limit with measurements.

### What This Test Must Answer

The output of this section must answer:

- How many concurrent sessions can one `c6a.xlarge` pod handle before latency or errors become unacceptable?
- Is `4` sessions per pod too conservative?
- Is `8`, `12`, `18`, or `24` sessions per pod safe?
- Does the bottleneck appear in CPU, memory, STT, LLM, TTS, LiveKit dispatch, workspace sync, or network?

### Preferred Method: Dedicated One-Pod Capacity Agent

Do not use the production `cheeko-agent1` pool for this test if avoidable. Create a temporary canary Deployment with the same image, resources, secrets, security context, and node selector as production, but with:

```text
agent name: cheeko-agent-capacity-test
replicas: 1
HPA: disabled for this canary
PICOCLAW_LIVEKIT_MAX_SESSIONS: temporarily raised to 24 or 36
```

Why:

- one pod receives all load for that test agent name
- HPA does not hide the real per-pod breaking point
- production `cheeko-agent1` traffic remains isolated
- the pod still runs on the same EKS node type and with the same resource limits

Then run:

```powershell
lk perf agent-load-test `
  --rooms <room-count> `
  --agent-name cheeko-agent-capacity-test `
  --echo-speech-delay 10s `
  --duration 5m
```

If the LiveKit CLI test does not include enough real room metadata for workspace/device behavior, use the same one-pod canary idea with the custom gateway/metadata-aware runner described later in this document.

### C6A Test Environment

Create a temporary `c6a.xlarge` node group and run the one-pod calibration there.

Temporary node group:

```text
name: picoclaw-ng-c6a-xlarge
instance type: c6a.xlarge
minSize: 1
desiredSize: 1
maxSize: 8
capacity: ON_DEMAND
labels:
  workload=picoclaw-livekit-capacity
  node.kubernetes.io/instance-type=c6a.xlarge
```

Pin the capacity-test Deployment to this node group with a node selector. Do not migrate production traffic until the test result is known.

Canary workload:

```text
agent name: cheeko-agent-capacity-test
replicas: 1
HPA: disabled for the canary
PICOCLAW_LIVEKIT_MAX_SESSIONS: start at 4, then raise for test rounds
nodeSelector:
  node.kubernetes.io/instance-type: c6a.xlarge
```

Expected decision:

```text
If c6a.xlarge passes the launch concurrency and latency target, migrate production to c6a.xlarge.
If c6a.xlarge passes only 4-8 sessions per pod, use Cerebrium-like safe mode and keep max pods at 8.
If c6a.xlarge fails below 4 sessions, do not migrate; investigate provider/runtime bottlenecks first.
```

### Fallback Method: Production Pool Maintenance Test

Only use this during a maintenance window.

1. Save the current HPA and Deployment state.
2. Temporarily disable or remove HPA for `picoclaw-livekit`.
3. Temporarily scale `picoclaw-livekit` to one replica.
4. Temporarily raise `PICOCLAW_LIVEKIT_MAX_SESSIONS`.
5. Run the per-pod ramp.
6. Restore the original HPA, replica count, and `PICOCLAW_LIVEKIT_MAX_SESSIONS`.

This method can affect real users and should not be the default.

### Per-Pod Ramp

Run each level for at least 5 minutes. Stop at the first sustained failure point.

| Step | Rooms on one pod | Purpose |
| --- | ---: | --- |
| P1 | 1 | verify canary joins and speaks |
| P2 | 4 | match Cerebrium `replica_concurrency=4` |
| P3 | 6 | current HPA comfort target for one production pod |
| P4 | 8 | candidate low-risk production target |
| P5 | 12 | current configured ceiling |
| P6 | 15 | test whether the current ceiling is conservative |
| P7 | 18 | higher Go/runtime/provider pressure |
| P8 | 24 | candidate next `max_sessions` value |
| P9 | 30 | stress point only if earlier phases are clean |

For each step, record:

- active rooms
- pod CPU and memory
- first audio p50/p95
- STT first final p50/p95
- LLM first token p50/p95
- TTS first audio p50/p95
- provider `429`/timeout/error count
- dropped turns or missing responses
- websocket closures
- workspace lock/restore/sync failures
- pod restarts or OOMKills

Commands while the one-pod test is running:

```powershell
kubectl --context $Context -n $Namespace get pods -o wide
kubectl --context $Context -n $Namespace top pods
kubectl --context $Context -n $Namespace logs -f <capacity-test-pod-name> --tail=200
```

If the canary uses a separate label such as `app=picoclaw-livekit-capacity`, use:

```powershell
kubectl --context $Context -n $Namespace get pods -l app=picoclaw-livekit-capacity -o wide
kubectl --context $Context -n $Namespace logs -f -l app=picoclaw-livekit-capacity --tail=200 --prefix=true
```

### Per-Pod Pass Criteria

A per-pod concurrency level is acceptable only if all of these are true:

- first audio p95 is `<= 4s` after STT final
- session failure rate is `< 1-2%`
- no repeated STT/LLM/TTS provider errors
- no pod restarts or OOMKills
- CPU is not sustained above `75-80%`
- memory is stable and comfortably below limit
- no repeated workspace lock/restore/sync failures
- user turns still complete naturally with no repeated dropped responses

The recommended production `PICOCLAW_LIVEKIT_MAX_SESSIONS` should be below the highest passing value. Use a safety margin.

Example decision:

```text
Highest clean one-pod result: 18 concurrent sessions
Observed stress/failure point: 24 concurrent sessions
Recommended max_sessions: 18
Recommended HPA target: 50%
Expected HPA scale trigger: about 9 sessions per pod
```

If 24 sessions pass cleanly:

```text
Recommended max_sessions candidate: 24
Expected 1-session reported load: 4.16%
Expected HPA scale trigger at 50%: about 12 sessions per pod
```

Do not raise `PICOCLAW_LIVEKIT_MAX_SESSIONS` only because CPU is low. Provider latency, stream stability, and first-audio p95 matter more for voice quality.

### Capacity Decision Table

Use this table after the `c6a.xlarge` one-pod test:

| Highest passing sessions per pod | Recommended config |
| ---: | --- |
| `< 4` | not ready; investigate before launch |
| `4` | Cerebrium-like safe mode: `max_sessions=4`, HPA target `60`, max pods `8` |
| `8` | balanced launch mode: `max_sessions=8`, HPA target `60`, max pods `8` |
| `12` | current-style mode: `max_sessions=12`, HPA target `50-60`, max pods `8-10` |
| `18` | optimized mode: `max_sessions=18`, HPA target `50`, max pods based on expected traffic |
| `24+` | high-density mode; use only if provider limits and p95 latency stay healthy |

For launch, choose one step below the first failing point.

Example:

```text
c6a.xlarge passes 12 sessions, fails or gets slow at 18.
Recommended launch setting: max_sessions=12 or 8, depending on p95 first-audio latency.
```

## Test Matrix

Run phases in order. Do not skip directly to 100 rooms.

| Phase | Rooms | Duration | Purpose | Expected result |
| --- | ---: | --- | --- | --- |
| A1 | 2 | 3m | CLI/tool sanity | agent joins, greeting heard, no config/metadata failure |
| A2 | 5 | 5m | small functional load | no errors, stable latency |
| B1 | 10 | 10m | baseline production-like load | no scaling required or mild scaling only |
| B2 | 15 | 10m | expected small launch load | stable latency, no provider errors |
| B3 | 25 | 10m | exceeds two-pod comfort target | HPA may begin adding pods |
| C1 | 40 | 10m | autoscaling validation | HPA scales, pods schedule cleanly |
| C2 | 60 | 15m | production-readiness target | stable under scaled pods |
| D1 | 75 | 10m | stress test | watch provider and pod pressure |
| D2 | 100 | 10m | upper stress test | expect to find limits; do not treat as first pass target |

Recommended command shape:

```powershell
lk perf agent-load-test `
  --rooms <room-count> `
  --agent-name cheeko-agent1 `
  --echo-speech-delay 10s `
  --duration <duration>
```

Examples:

```powershell
lk perf agent-load-test --rooms 5 --agent-name cheeko-agent1 --echo-speech-delay 10s --duration 5m
lk perf agent-load-test --rooms 25 --agent-name cheeko-agent1 --echo-speech-delay 10s --duration 10m
lk perf agent-load-test --rooms 60 --agent-name cheeko-agent1 --echo-speech-delay 10s --duration 15m
```

If the CLI supports attributes for your installed version, tag runs:

```powershell
lk perf agent-load-test `
  --rooms 25 `
  --agent-name cheeko-agent1 `
  --echo-speech-delay 10s `
  --duration 10m `
  --attribute test_id=load-20260611-b3 `
  --attribute env=picoclaw-dev
```

## Monitoring During Each Run

Open separate terminals.

Agent logs:

```powershell
kubectl --context $Context -n $Namespace logs -f -l app=picoclaw-livekit `
  --all-containers=true `
  --tail=200 `
  --since=10m `
  --max-log-requests=10 `
  --prefix=true
```

HPA watch:

```powershell
kubectl --context $Context -n $Namespace get hpa picoclaw-livekit -w
```

Pods and scheduling:

```powershell
kubectl --context $Context -n $Namespace get pods -l app=picoclaw-livekit -o wide -w
```

Cluster nodes:

```powershell
kubectl --context $Context get nodes -w
```

Pod resources:

```powershell
kubectl --context $Context -n $Namespace top pods
kubectl --context $Context top nodes
```

Cluster Autoscaler logs:

```powershell
kubectl --context $Context -n kube-system logs -f deployment/cluster-autoscaler --tail=120
```

Events:

```powershell
kubectl --context $Context -n $Namespace get events --sort-by=.lastTimestamp
```

LiveKit Cloud dashboard:

- agent session count
- agent load
- join latency percentiles
- dispatch failures
- room/session errors
- Agent Insights recordings/transcripts if available

Provider dashboards:

- STT concurrency, errors, latency
- LLM request rate, time to first token, `429`/timeout errors
- TTS latency, concurrency, `429`/timeout errors

## Log Markers To Track

Healthy markers:

```text
Job assignment received
Resolved per-session provider selection
Acquired manager distributed workspace lock
workspace fast-path restore completed
Joined room
Published local TTS track
Audio track subscribed
STT stream opened
TEN VAD speech start detected
LLM request config
Turn latency summary
Session quality summary
workspace-sync uploaded to manager
Released manager workspace lock
```

Important latency fields:

```text
stt_first_final_ms
llm_first_token_ms
llm_final_token_ms
tts_first_audio_ms
tts_first_audio_from_stt_ms
turn_total_e2e_ms
avg_ttft_ms
median_ttft_ms
```

For voice UX, prioritize:

- time until first agent audio
- STT final latency
- LLM first token latency
- TTS first audio latency
- interruption recovery

`turn_total_e2e_ms` may include completion/bookkeeping after the user already heard audio, so do not use it alone as the perceived latency metric.

Noisy but not automatically fatal:

```text
Tool registration overwrites existing tool
Forced required workspace file tools for LiveKit agent
Received abort from gateway
```

Investigate immediately:

```text
401 Unauthorized
429
timeout
websocket abnormal closure
workspace lock timeout
pod OOMKilled
CrashLoopBackOff
ImagePullBackOff
FailedScheduling
custom metric <unknown>
```

## Metrics To Record

For every phase, record:

| Metric | Source |
| --- | --- |
| room count | CLI command |
| duration | CLI command |
| agent join delay p50/p95/p99 | CLI output / LiveKit dashboard |
| first greeting success rate | CLI/dashboard/log review |
| user-perceived first audio p50/p95 | logs / Agent Insights |
| STT first final p50/p95 | logs/provider dashboard |
| LLM first token p50/p95 | logs/provider dashboard |
| TTS first audio p50/p95 | logs/provider dashboard |
| session failure rate | CLI/dashboard/logs |
| dispatch errors | LiveKit dashboard/logs |
| provider `429`/timeout/errors | provider dashboards/logs |
| pod replicas min/max | HPA |
| pending pod count and duration | Kubernetes |
| node count min/max | Kubernetes/AWS |
| pod CPU/memory p95 | metrics-server/Prometheus |
| restarts/OOMKills | Kubernetes |
| workspace lock/restore/sync errors | logs |

## Pass Criteria

For the initial production-readiness target, use these thresholds:

| Signal | Target |
| --- | --- |
| agent join delay p95 | `< 5s` |
| first greeting success | `>= 99%` |
| user-perceived first audio p95 | `<= 4s` after STT final |
| session failure rate | `< 1-2%` |
| provider quota errors | `0` |
| pod restarts/OOMKills | `0` |
| sustained pending pods | `0` after autoscaler has time to react |
| HPA behavior | scales up at load, scales down after stabilization |
| Cluster Autoscaler behavior | adds nodes when pods cannot schedule, later removes empty nodes |
| workspace errors | no repeated lock/restore/sync failures |

If Phase C2 (`60` rooms for `15m`) passes, the deployment is reasonable for a controlled launch where expected concurrency is under that level.

If Phase D2 (`100` rooms) fails, that does not automatically block launch. It identifies the next bottleneck: provider quota, HPA max, node count, pod resources, LiveKit plan, or app latency.

## Stop Conditions

Stop the current run if any of these occur:

- LiveKit dispatch/join errors spike.
- Provider returns repeated `429`, `401`, `5xx`, or timeout errors.
- Pods restart, OOMKill, or enter CrashLoopBackOff.
- Pending pods last longer than `3m`.
- CPU is sustained above `85%`.
- Memory approaches container limits.
- User-perceived first audio p95 is consistently worse than `6s`.
- Workspace lock failures repeat across rooms.
- LiveKit Cloud or provider dashboard shows quota exhaustion.

Stop command:

- interrupt the `lk perf agent-load-test` process with `Ctrl+C`
- confirm rooms end in LiveKit dashboard
- watch pod/session load drop

After stopping:

```powershell
kubectl --context $Context -n $Namespace get hpa picoclaw-livekit -o wide
kubectl --context $Context -n $Namespace get pods -l app=picoclaw-livekit -o wide
kubectl --context $Context get nodes
```

Remember HPA scale-down stabilization is `900s`, so pods may remain elevated for about 15 minutes after load drops.

## Result Template

Create one result note per phase:

```text
Test ID:
Date/time:
Operator:
LiveKit project:
Agent image digest:
Rooms:
Duration:
Echo speech delay:
Load generator host:

CLI summary:

LiveKit dashboard summary:
- join delay p50/p95/p99:
- session count:
- dispatch errors:

Kubernetes summary:
- replicas before/during/after:
- nodes before/during/after:
- pending pods:
- restarts:
- CPU/memory:

Provider summary:
- STT errors/latency:
- LLM errors/latency:
- TTS errors/latency:

Log summary:
- first audio p50/p95:
- STT first final p50/p95:
- LLM first token p50/p95:
- TTS first audio p50/p95:
- workspace errors:
- disconnects/timeouts:

Pass/fail:
Next action:
```

## If CLI Metadata Is Not Enough

If `lk perf agent-load-test` does not reproduce real product behavior, build a custom runner.

Runner requirements:

- create LiveKit rooms with the same naming convention as the gateway
- include representative room metadata:
  - device identity
  - active skills
  - language
  - provider selection if applicable
  - session identifiers
- dispatch `cheeko-agent1`
- join one synthetic audio participant per room
- publish realistic short speech audio or echo agent speech
- ramp sessions gradually
- collect per-room join, first greeting, first response, disconnect, and error metrics

Implementation options:

- LiveKit Server SDK script to create rooms and dispatch agents
- gateway-driven script that starts sessions through the same API path real devices use
- multiple cloud VMs for higher concurrency

Use the same ramp and stop conditions as the CLI plan.

## Remediation Decision Tree

If join delay is high:

- check LiveKit dashboard agent join percentiles
- check HPA scale-up speed
- check pending pods and Cluster Autoscaler logs
- consider higher `minReplicas`, higher node `desiredSize`, or lower pod requests if resources are over-reserved

If first audio is slow:

- break down STT final, LLM first token, and TTS first audio
- tune VAD endpoint, provider choice, response length, streaming, and TTS settings
- check provider region and quotas

If HPA does not scale:

- check custom metric API
- check Prometheus scrape and adapter mapping
- check HPA events
- verify metric labels include namespace and pod

If pods cannot schedule:

- check node selector for the active test node type, such as `c6a.xlarge`
- check node group max size
- check vCPU quota
- check subnet capacity
- check Cluster Autoscaler IAM and discovery tags

If provider errors appear:

- reduce target concurrency
- request quota increase
- add provider-side concurrency controls/retry budgets
- consider fallback providers only after measuring the primary bottleneck

If workspace lock errors appear:

- check Manager API health
- verify lock routes exist
- check `MANAGER_API_URL`
- check lock TTL and release behavior under disconnects

## Final Readiness Report

After all phases, produce a short report with:

- highest passing concurrency
- first failing concurrency
- bottleneck category
- recommended production concurrency limit
- recommended HPA min/max
- recommended `PICOCLAW_LIVEKIT_MAX_SESSIONS`
- provider quota changes needed
- AWS node group changes needed
- launch decision: ready, ready with cap, or not ready

Do not raise launch concurrency just because the configured ceiling says it is possible. Use the measured latency and failure rate from this plan.
