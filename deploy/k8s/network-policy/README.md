# LiveKit Agent NetworkPolicy

Status: staged, not applied.

The policy in this folder restricts the LiveKit agent pods to:

- ingress from monitoring to port `8192` for metrics/health scraping
- DNS egress to CoreDNS
- outbound TCP `443` for LiveKit Cloud and provider APIs
- outbound TCP `5432` for Postgres/STT database access

## Why this is not applied yet

The current EKS cluster is using AWS VPC CNI with network policy enforcement disabled:

```text
--enable-network-policy=false
```

There is also no managed `vpc-cni` addon currently reported by EKS. Until a policy engine is enabled, Kubernetes accepts the `NetworkPolicy` object but does not enforce it.

## Safe enablement order

1. Convert or install the EKS `vpc-cni` addon with network policy support, or install another supported policy engine.
2. Confirm `aws-node`/node agent is running with network policy enabled.
3. Apply the policy in a maintenance window:

```powershell
kubectl apply --dry-run=server -f deploy/k8s/network-policy/livekit-networkpolicy.yaml
kubectl apply -f deploy/k8s/network-policy/livekit-networkpolicy.yaml
```

4. Verify:

```powershell
kubectl -n picoclaw-dev get networkpolicy
kubectl -n picoclaw-dev logs deployment/picoclaw-livekit --tail=80
kubectl -n picoclaw-dev get hpa picoclaw-livekit -o wide
```

5. Confirm a real voice session can connect to LiveKit Cloud, restore workspace from Manager API, use STT/TTS/provider APIs, and sync back.

## Rollback

```powershell
kubectl -n picoclaw-dev delete networkpolicy picoclaw-livekit-egress
```
