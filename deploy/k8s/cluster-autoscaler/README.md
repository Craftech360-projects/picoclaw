# Cluster Autoscaler Notes

Cluster: `picoclaw-eks`
Region: `ap-south-2`

## Current node groups

- Active agent node group: `picoclaw-ng-c7i-xlarge`
  - Instance type: `c7i.xlarge`
  - Labels: `workload=picoclaw-livekit`, `node.kubernetes.io/instance-type=c7i.xlarge`
  - Scaling config after rollout: `minSize=3`, `desiredSize=3`, `maxSize=10`
- Old node group: `picoclaw-ng-m7i-xlarge`
  - Scaling config after migration: `minSize=0`, `desiredSize=0`, `maxSize=1`
  - Cluster Autoscaler discovery tags were removed so it is not used for new capacity.

## Capacity note

The account's EC2 On-Demand Standard quota in `ap-south-2` is 64 vCPU.

A quota increase request was submitted on 2026-06-10:

- Quota: `Running On-Demand Standard (A, C, D, H, I, M, R, T, Z) instances`
- Quota code: `L-1216C47A`
- Requested value: `64`
- Request ID: `21d765a1a4604c418ee171668e5e82b3wjgryKwf`
- Final status checked on 2026-06-10: approved, quota value `64`

With three `c7i.xlarge` EKS nodes and two non-EKS `t3.small` instances, current usage is
approximately 16 vCPU. The approved 64 vCPU quota leaves room for Cluster Autoscaler to
launch additional `c7i.xlarge` nodes up to the configured node group `maxSize: 10`.
