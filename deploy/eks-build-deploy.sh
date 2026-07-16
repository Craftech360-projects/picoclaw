#!/usr/bin/env bash
# Build the picoclaw-livekit image from the CURRENT branch and deploy to EKS.
# Manual deploy path (no CI builds the ECR image). See EKS_DEPLOY_GUIDE.md.
#
# Usage:  bash deploy/eks-build-deploy.sh
# Run from the repo root with the branch you want to ship checked out.
set -euo pipefail

REGION="ap-south-2"
ACCOUNT="382188660865"
ECR="${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/picoclaw-livekit"
NS="picoclaw-dev"
DEPLOY="picoclaw-livekit"
CONTAINER="livekit"

cd "$(git rev-parse --show-toplevel)"
BRANCH="$(git branch --show-current)"
IMG="${ECR}:$(echo "$BRANCH" | tr '/' '-')-$(date +%Y%m%d-%H%M)"

echo "==> Branch: $BRANCH"
echo "==> Image:  $IMG"

# 1) Ensure Dockerfile.eks COPY sources exist (cheeko.tmpl lives only on EKS branch).
if [ ! -f prompts/cheeko.tmpl ]; then
  echo "==> prompts/cheeko.tmpl missing; copying from origin/EKS"
  mkdir -p prompts
  git show origin/EKS:prompts/cheeko.tmpl > prompts/cheeko.tmpl
fi
for f in third_party/ten-vad/lib/Linux/x64/libten_vad.so workspace-template prompts/cheeko.tmpl; do
  [ -e "$f" ] || { echo "MISSING required file: $f" >&2; exit 1; }
done

# 2) Build (heavy: cgo + TEN-VAD).
echo "==> docker build (this takes a few minutes)"
docker build -f Dockerfile.eks -t "$IMG" .

# 3) ECR login + push.
echo "==> ECR login + push"
aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com"
docker push "$IMG"

# 4) Roll the deployment.
echo "==> set image + rollout"
kubectl -n "$NS" set image "deployment/${DEPLOY}" "${CONTAINER}=${IMG}"
kubectl -n "$NS" rollout status "deployment/${DEPLOY}" --timeout=180s

# 5) Verify.
echo "==> deployed image:"
kubectl -n "$NS" get deployment "$DEPLOY" -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n "$NS" get pods -l app=picoclaw-livekit -o wide
echo "==> done. Rollback with: kubectl -n $NS rollout undo deployment/$DEPLOY"
