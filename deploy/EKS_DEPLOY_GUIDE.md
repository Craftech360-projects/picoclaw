# picoclaw-livekit — EKS Deploy Guide (manual build → ECR → rollout)

Step-by-step for building the LiveKit voice-agent image from a local branch and
deploying it to the EKS cluster. **This is a manual process** — there is no CI
pipeline that builds the ECR image (the `deploy.yml` workflow is SSH→pm2, and
`docker-build.yml` pushes to GHCR/DockerHub, not ECR).

---

## 0. What you're deploying to

| Thing | Value |
|---|---|
| Cluster (EKS) | `arn:aws:eks:ap-south-2:382188660865:cluster/picoclaw-eks` |
| Region | `ap-south-2` |
| Namespace | `picoclaw-dev` |
| Deployment | `picoclaw-livekit` |
| Container name | `livekit` |
| ECR repo | `382188660865.dkr.ecr.ap-south-2.amazonaws.com/picoclaw-livekit` |
| Dockerfile | `Dockerfile.eks` (repo root) |
| Manager API (used by pods) | `http://139.59.7.72:8002/toy` (set via `picoclaw-secrets`) |

The image is built from **whatever branch/working tree you have checked out**
(the Docker build context is `.`). So `git checkout <branch>` first.

---

## 1. Prerequisites (one-time)

- **Docker Desktop** running (Linux engine). Check: `docker info`.
- **AWS CLI v2** configured with access to account `382188660865`.
  Check: `aws sts get-caller-identity --query Account --output text` → `382188660865`.
- **kubectl** with the EKS context available.
  Check: `kubectl config current-context` → the `picoclaw-eks` arn above.
  If missing: `aws eks update-kubeconfig --region ap-south-2 --name picoclaw-eks`.

---

## 2. Pick the branch + one required file fix

```bash
git checkout <your-branch>          # e.g. feat/smallest-tts
```

**GOTCHA (important):** `Dockerfile.eks` copies `prompts/cheeko.tmpl`, but that
file lives **only on the `EKS` branch**, not on `main`/feature branches. If it's
missing the build fails at `COPY prompts/cheeko.tmpl`. Copy it in:

```bash
mkdir -p prompts
git show origin/EKS:prompts/cheeko.tmpl > prompts/cheeko.tmpl
```

Verify all three `Dockerfile.eks` COPY sources exist:

```bash
for f in third_party/ten-vad/lib/Linux/x64/libten_vad.so workspace-template prompts/cheeko.tmpl; do
  test -e "$f" && echo "OK  $f" || echo "MISSING  $f"
done
```

---

## 3. Build the image (heavy — several minutes)

The build is cgo + TEN-VAD (libc++), so it is CPU/time heavy. Tag with the
branch + timestamp so images are traceable.

```bash
IMG="382188660865.dkr.ecr.ap-south-2.amazonaws.com/picoclaw-livekit:$(git branch --show-current | tr '/' '-')-$(date +%Y%m%d-%H%M)"
echo "Building $IMG"
docker build -f Dockerfile.eks -t "$IMG" .
```

On success the last lines show `writing image sha256:…` and `naming to …:<tag>`.

---

## 4. Log in to ECR + push

The login token lasts ~12h; re-run if it expires.

```bash
aws ecr get-login-password --region ap-south-2 \
  | docker login --username AWS --password-stdin 382188660865.dkr.ecr.ap-south-2.amazonaws.com

docker push "$IMG"
```

---

## 5. Roll the deployment

```bash
kubectl -n picoclaw-dev set image deployment/picoclaw-livekit livekit="$IMG"
kubectl -n picoclaw-dev rollout status deployment/picoclaw-livekit --timeout=180s
```

Rolling update: new pods come up, old pods **drain gracefully** (they won't
terminate until active voice sessions finish or the drain timeout elapses), then
terminate. Ends with `deployment "picoclaw-livekit" successfully rolled out`.

---

## 6. Verify

```bash
# pods healthy + on the new image
kubectl -n picoclaw-dev get pods -l app=picoclaw-livekit -o wide
kubectl -n picoclaw-dev get deployment picoclaw-livekit \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'

# readiness / restarts (want ready=true, restarts=0)
kubectl -n picoclaw-dev get pods -l app=picoclaw-livekit \
  -o jsonpath='{range .items[*]}{.metadata.name}{"  ready="}{.status.containerStatuses[0].ready}{"  restarts="}{.status.containerStatuses[0].restartCount}{"\n"}{end}'
```

Optional — confirm the agent starts a real session (connect a device, then):

```bash
kubectl -n picoclaw-dev logs <pod> --since=10m | grep -iE "Hydrated|LLM request config|tts_first_audio"
```

---

## 7. Rollback (if the new image misbehaves)

```bash
kubectl -n picoclaw-dev rollout undo deployment/picoclaw-livekit
kubectl -n picoclaw-dev rollout status deployment/picoclaw-livekit
```

To pin back to a specific known-good tag:

```bash
kubectl -n picoclaw-dev set image deployment/picoclaw-livekit livekit=<...>/picoclaw-livekit:<known-good-tag>
```

---

## 8. Common gotchas

- **`COPY prompts/cheeko.tmpl: not found`** → do step 2 (copy from `origin/EKS`).
- **`docker info` errors / build hangs** → Docker Desktop isn't running.
- **`no basic auth credentials` on push** → ECR login expired; re-run step 4.
- **Old pod stuck `Terminating` for a while** → normal graceful drain; it waits
  for the active session / `PICOCLAW_LIVEKIT_DRAIN_TIMEOUT_SECONDS`.
- **Branch lineage caveat:** feature branches off `main` do **not** include the
  `EKS` branch's runtime hardening (distributed workspace locking, drain
  refactor). Deploying them is fine for testing but is not the `EKS`-branch code.
  Long-term, port changes onto `EKS` and build from there.
- **Force-flush pod-local workspace caches** (rarely needed; workspaces are
  restored from the manager DB anyway):
  `kubectl -n picoclaw-dev rollout restart deployment/picoclaw-livekit`

---

## 9. One-shot script

For convenience, `deploy/eks-build-deploy.sh` runs steps 2–6 end to end.
Usage: `bash deploy/eks-build-deploy.sh` (from repo root, with the target branch
checked out).
