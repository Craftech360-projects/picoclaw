#!/usr/bin/env bash
# Server-side deploy for picoclaw-livekit. Source is already rsynced by CI.
set -euo pipefail
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
cd /opt/picoclaw

echo "==> build picoclaw-livekit (cgo)"
make build-livekit

echo "==> (re)start via pm2"
pm2 startOrReload /opt/ecosystem.config.js --only picoclaw-livekit --update-env
pm2 save
echo "==> picoclaw deploy done"
