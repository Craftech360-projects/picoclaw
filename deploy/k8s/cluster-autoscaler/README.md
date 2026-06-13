# Cluster Autoscaler Notes

Cluster: `picoclaw-eks`
Region: `ap-south-2`

## Current node groups

- Active agent node group: `picoclaw-ng-c6a-large`
  - Instance type: `c6a.large`
  - Labels: `workload=picoclaw-livekit`, `node.kubernetes.io/instance-type=c6a.large`
  - Scaling config after production migration: `minSize=2`, `desiredSize=2`, `maxSize=5`
- Previous agent node group: `picoclaw-ng-c7i-xlarge`
  - Scaling config after migration: `minSize=0`, `desiredSize=0`, `maxSize=1`
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

With two `c6a.large` EKS nodes and two non-EKS `t3.small` instances, current usage is
approximately 6 vCPU. The approved 64 vCPU quota leaves room for Cluster Autoscaler to
launch additional `c6a.large` nodes up to the configured node group `maxSize: 5`.
