# Monitoring Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace an alert set nobody trusts with three layers that each see what they can actually see — Kuma for external reachability, Prometheus for in-cluster decay, on-box scripts for process health — rolled out on dev first, then prod.

**Architecture:** Kuma shrinks to blackbox probing and a status page, targeting public hostnames rather than raw IPs. The Prometheus already running in EKS (currently only feeding the HPA) gains durable storage and Alertmanager, and takes over decay detection. DO-box process health uses Kuma push monitors driven by an on-box cron script, as a deliberate stopgap. All three notify one Telegram channel.

**Tech Stack:** Uptime Kuma 2.5.3 (Docker, EC2 `16.112.52.71`), Prometheus v3.11.3 / Helm chart `prometheus-29.7.0` (EKS `picoclaw-eks`, ns `monitoring`), Alertmanager, bash + cron + jq on Ubuntu DO boxes, AWS CLI for security groups.

**Spec:** `docs/superpowers/specs/2026-08-31-monitoring-redesign-design.md`

## Global Constraints

- **Kuma host:** `aws-kuma-production` = `16.112.52.71`, ap-south-2, instance `i-0c1d60b5df6d3220d`, SSH user `ec2-user`, key `~/.ssh/kuma-ap-south-2.pem`. SSH alias already in `~/.ssh/config`.
- **Kuma security group:** `sg-0ca72b10de2f2e764`.
- **Kuma data dir on host:** `/opt/uptime-kuma/data`, bind-mounted to `/app/data` in the container. Compose file at `/opt/uptime-kuma/docker-compose.yml`.
- **Root disk is 8 GB at 77% used (1.9 GB free).** Every task that pulls an image or writes a backup must check free space first. Do not let this fill.
- **`ADMIN_CIDR` must be supplied by the user before Task 1** — the CIDR allowed to reach Kuma's ports 22 and 3001. Get the current public IP with `curl -s https://checkip.amazonaws.com`. Written below as `${ADMIN_CIDR}`; substitute the real value, never commit it.
- **Kuma has no REST API for monitor CRUD.** Only `/api/push/:token`, `/api/badge/*`, `/api/entry-page`, and the status-page router exist. Monitors are created **by hand in the UI**. `uptime-kuma-api` on PyPI is v1-only and unmaintained since 2023 — do not use it. The checked-in inventory table is the source of truth; the UI is only the transport.
- **Dev box:** `64.227.170.31` (SSH alias configured, user `root`), public hostname `otadev.cheekoai.in` fronted by Caddy.
- **Prod DO box:** `139.59.7.72`. **Do not touch prod until Task 12.**
- **EKS:** cluster `picoclaw-eks`, ap-south-2. **Namespace `picoclaw-dev` is production** despite its name.
- **Prometheus has no `kube-state-metrics` and no `node-exporter`.** Do not write rules using `kube_*` or `node_*` metrics. Available: `container_start_time_seconds` (cadvisor), `up`, and `picoclaw_livekit_session_load_percent` (the app's own exporter).
- **Never delete heartbeat history** to free disk. Fix retention settings instead.

---

## Task 1: Back up Kuma and lock down network access

Nothing else in this plan is safe until there is a restorable backup and the box is off the public internet. API keys for the dependency probes (Task 6) land in `kuma.db` as plaintext, so the lockdown gates that work.

**Status: COMPLETED 2026-08-31.** Deviations from the original plan, both discovered during execution:

- **An Elastic IP was allocated first.** `18.61.233.60` was an auto-assigned public IP with no EIP
  behind it, so a single instance stop/start would have silently broken every hardcoded reference
  built in later tasks. Now `16.112.52.71` (`eipalloc-0f0a1b78be1cf3404`), permanent.
- **systemd timer instead of cron.** `/etc/cron.d` exists on Amazon Linux 2023 but `cronie` is not
  installed, so a cron entry would have been silently dead — a backup job that never runs is worse
  than none. systemd was already running, so a timer needed no new package.

**Files:**
- Create: `/opt/uptime-kuma/backup.sh` (on `aws-kuma-production`)
- Create: `/etc/systemd/system/kuma-backup.{service,timer}` (on `aws-kuma-production`)
- Modify: security group `sg-0ca72b10de2f2e764` (AWS, not a file)

**Interfaces:**
- Consumes: nothing.
- Produces: `/opt/uptime-kuma/backups/kuma-YYYYMMDD-HHMMSS.db.gz` — a restorable Kuma database. Task 2 requires one of these to exist before upgrading.

- [ ] **Step 1: Confirm free disk before writing anything**

```bash
ssh aws-kuma-production 'df -h / | awk "NR==2 {print \$4\" free\"}"'
```

Expected: at least `1.0G` free. If under that, stop and reclaim space first — the backup plus the Task 2 image pull need room.

- [ ] **Step 2: Take a manual online backup and verify it is readable**

SQLite's `.backup` is safe on a live database, so Kuma keeps running.

```bash
ssh aws-kuma-production 'sudo mkdir -p /opt/uptime-kuma/backups && sudo docker exec uptime-kuma sqlite3 /app/data/kuma.db ".backup /app/data/manual-backup.db" && sudo mv /opt/uptime-kuma/data/manual-backup.db /opt/uptime-kuma/backups/kuma-manual.db && sudo sqlite3 /opt/uptime-kuma/backups/kuma-manual.db "pragma integrity_check; select count(*) from monitor;"'
```

Expected: `ok` followed by `16`. If integrity_check prints anything but `ok`, stop and investigate — do not proceed to the upgrade.

- [ ] **Step 3: Copy the backup off the instance**

A backup that only exists on the box it protects is not a backup.

```bash
scp aws-kuma-production:/opt/uptime-kuma/backups/kuma-manual.db "D:/cheeko-backend/.tmp/aws-kuma/kuma-backup-$(date +%Y%m%d).db"
```

Expected: a local file of roughly 11 MB.

- [ ] **Step 4: Write the recurring backup script**

```bash
ssh aws-kuma-production 'sudo tee /opt/uptime-kuma/backup.sh > /dev/null' <<'EOF'
#!/usr/bin/env bash
# Nightly Uptime Kuma backup. Online .backup, so no downtime.
set -euo pipefail
DEST=/opt/uptime-kuma/backups
STAMP=$(date +%Y%m%d-%H%M%S)
mkdir -p "$DEST"

# Refuse to run if the disk is nearly full; a partial backup is worse than none.
AVAIL_KB=$(df --output=avail / | tail -1)
if [ "$AVAIL_KB" -lt 524288 ]; then
  echo "kuma-backup: under 512MB free, refusing to run" >&2
  exit 1
fi

docker exec uptime-kuma sqlite3 /app/data/kuma.db ".backup /app/data/backup-tmp.db"
mv /opt/uptime-kuma/data/backup-tmp.db "$DEST/kuma-$STAMP.db"
gzip -f "$DEST/kuma-$STAMP.db"

# Keep 7 days.
find "$DEST" -name 'kuma-*.db.gz' -mtime +7 -delete
EOF
ssh aws-kuma-production 'sudo chmod +x /opt/uptime-kuma/backup.sh'
```

- [ ] **Step 5: Run the script once and verify it produces a valid backup**

```bash
ssh aws-kuma-production 'sudo /opt/uptime-kuma/backup.sh && ls -la /opt/uptime-kuma/backups/ && sudo sh -c "gunzip -c /opt/uptime-kuma/backups/$(ls /opt/uptime-kuma/backups | grep gz | tail -1) > /tmp/verify.db" && sudo sqlite3 /tmp/verify.db "pragma integrity_check;" && sudo rm /tmp/verify.db'
```

Expected: a `.db.gz` file listed, then `ok`.

- [x] **Step 6: Schedule it nightly via a systemd timer**

Do **not** use `/etc/cron.d` on this host. The directories exist, but `cronie` is not installed on
Amazon Linux 2023, so a cron entry is accepted and never runs.

```bash
ssh aws-kuma-production 'set -e
sudo tee /etc/systemd/system/kuma-backup.service > /dev/null <<UNIT
[Unit]
Description=Uptime Kuma nightly backup

[Service]
Type=oneshot
ExecStart=/opt/uptime-kuma/backup.sh
UNIT
sudo tee /etc/systemd/system/kuma-backup.timer > /dev/null <<UNIT
[Unit]
Description=Run Uptime Kuma backup nightly

[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true

[Install]
WantedBy=timers.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now kuma-backup.timer
sudo systemctl list-timers kuma-backup.timer --no-pager'
```

Expected: the timer listed with a NEXT time. Trigger it once with
`sudo systemctl start kuma-backup.service` and confirm a new `.db.gz` appears — scheduling a job
without ever running it is how backups turn out to be broken on the day they are needed.

- [x] **Step 6b: Allocate an Elastic IP before locking down**

The instance had no EIP, so its public address was temporary. Everything downstream hardcodes it.

```bash
ALLOC=$(aws ec2 allocate-address --region ap-south-2 --domain vpc --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=Name,Value=uptime-kuma}]' --query 'AllocationId' --output text)
aws ec2 associate-address --region ap-south-2 --allocation-id "$ALLOC" --instance-id i-0c1d60b5df6d3220d
aws ec2 describe-instances --region ap-south-2 --instance-ids i-0c1d60b5df6d3220d --query 'Reservations[0].Instances[0].PublicIpAddress' --output text
```

Then update `~/.ssh/config` to the new address and confirm SSH before touching the security group.

- [ ] **Step 7: Restrict the security group to the admin CIDR**

Revoke the world-open rules, then add scoped ones. Order matters: add first, then revoke, so an error does not lock you out mid-change.

```bash
aws ec2 authorize-security-group-ingress --region ap-south-2 --group-id sg-0ca72b10de2f2e764 --ip-permissions "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=${ADMIN_CIDR},Description=admin-ssh}]" "IpProtocol=tcp,FromPort=3001,ToPort=3001,IpRanges=[{CidrIp=${ADMIN_CIDR},Description=admin-kuma-ui}]"
```

- [ ] **Step 8: Verify you still have access before revoking**

```bash
ssh aws-kuma-production 'echo still-reachable' && curl -s -o /dev/null -w "%{http_code}\n" -m 10 http://16.112.52.71:3001
```

Expected: `still-reachable` then `302` or `200`. **If either fails, stop — do not run Step 9.**

- [ ] **Step 9: Revoke the world-open rules**

```bash
aws ec2 revoke-security-group-ingress --region ap-south-2 --group-id sg-0ca72b10de2f2e764 --ip-permissions "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=0.0.0.0/0}]" "IpProtocol=tcp,FromPort=3001,ToPort=3001,IpRanges=[{CidrIp=0.0.0.0/0}]"
```

- [ ] **Step 10: Confirm the lockdown**

```bash
aws ec2 describe-security-groups --region ap-south-2 --group-ids sg-0ca72b10de2f2e764 --query 'SecurityGroups[0].IpPermissions[].{port:FromPort,cidrs:IpRanges[].CidrIp}' --output json
```

Expected: ports 22 and 3001, each listing only `${ADMIN_CIDR}`. No `0.0.0.0/0` anywhere.

- [ ] **Step 11: Record the runbook change**

Update `D:/cheeko-backend/README_UPTIME_KUMA.md` — it currently describes the retired DO-hosted instance. Replace the "Uptime Kuma Runtime" section with:

```markdown
## Uptime Kuma Runtime

- Host: EC2 `16.112.52.71` (`aws-kuma-production`), ap-south-2, instance `i-0c1d60b5df6d3220d`
- Container name: `uptime-kuma`
- UI: `http://16.112.52.71:3001` — reachable only from the admin CIDR
- Data dir: `/opt/uptime-kuma/data` (host) -> `/app/data` (container)
- Backups: `/opt/uptime-kuma/backups`, nightly at 02:30 via `/etc/cron.d/kuma-backup`, 7-day retention
```

- [ ] **Step 12: Commit**

```bash
git -C D:/cheeko-backend add README_UPTIME_KUMA.md
git -C D:/cheeko-backend commit -m "docs(monitoring): point Kuma runbook at the EC2 instance, document backups"
```

---

## Task 2: Upgrade Kuma 2.3.2 to a pinned 2.5.3

The container tracks the floating `:2` tag but has never been re-pulled, so it has drifted four months behind. Pin an exact tag so the version is a decision rather than an accident.

**Status: COMPLETED 2026-08-31.** Three deviations, all found by verifying rather than assuming:

- **There is no `docker compose` on this host at all** — no v1 binary, no CLI plugin, and
  `docker-compose-plugin` is not in the AL2023 repos. `/opt/uptime-kuma/docker-compose.yml` was
  decorative; the container had been started by hand. Replaced it with `/opt/uptime-kuma/run.sh`,
  which is the real definition and is what future upgrades should edit.
- **8 GB root volume was too small.** The 2.5.x image bundles MariaDB and is 1.7 GB, so pulling the
  new one alongside the old one ran out of space mid-extraction. Reclaimed 2.1 GB of leftover
  `~/.vscode-server`, then grew the volume 8 GB → 16 GB.
- **The filesystem is xfs, not ext4** — the grow is `growpart` + `xfs_growfs`, not `resize2fs`.

**Files:**
- Create: `/opt/uptime-kuma/run.sh` (on `aws-kuma-production`)
- Delete: `/opt/uptime-kuma/docker-compose.yml` — misleading, nothing can run it

**Interfaces:**
- Consumes: a verified backup from Task 1.
- Produces: Kuma 2.5.3 running with database schema migrated. Tasks 4–7 depend on 2.x push semantics.

- [ ] **Step 1: Confirm a fresh backup exists**

```bash
ssh aws-kuma-production 'ls -la /opt/uptime-kuma/backups/ | tail -3; df -h / | awk "NR==2 {print \$4\" free\"}"'
```

Expected: a backup from today, and at least 1 GB free. **There is no downgrade path** once 2.5.3 migrates the schema — if either check fails, go back to Task 1.

- [ ] **Step 2: Record the current version for comparison**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sh -c "grep -m1 version /app/package.json"; sudo docker inspect uptime-kuma --format "{{.Config.Image}}"'
```

Expected: `"version": "2.3.2",` and `louislam/uptime-kuma:2`.

- [ ] **Step 3: Pin the exact tag in the compose file**

```bash
ssh aws-kuma-production 'sudo sed -i "s|louislam/uptime-kuma:2$|louislam/uptime-kuma:2.5.3|" /opt/uptime-kuma/docker-compose.yml && cat /opt/uptime-kuma/docker-compose.yml'
```

Expected: the `image:` line now reads `louislam/uptime-kuma:2.5.3`.

- [ ] **Step 4: Pull the new image and apply**

```bash
ssh aws-kuma-production 'cd /opt/uptime-kuma && sudo docker compose pull && sudo docker compose up -d'
```

- [ ] **Step 5: Verify the upgrade took and the data survived**

```bash
ssh aws-kuma-production 'sleep 20; sudo docker exec uptime-kuma sh -c "grep -m1 version /app/package.json"; sudo docker exec uptime-kuma sqlite3 /app/data/kuma.db "select count(*) from monitor; select count(*) from heartbeat;"; sudo docker ps --filter name=uptime-kuma --format "{{.Status}}"'
```

Expected: `"version": "2.5.3",`, monitor count `16`, a heartbeat count at or above 25989, and status `Up ... (healthy)`.

- [ ] **Step 6: Confirm the UI serves and monitors resumed**

```bash
curl -s -o /dev/null -w "%{http_code}\n" -m 10 http://16.112.52.71:3001
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 /app/data/kuma.db "select datetime(max(time)) from heartbeat;"'
```

Expected: `302` or `200`, and a heartbeat timestamp within the last two minutes. If heartbeats stopped, restore from backup: stop the container, `gunzip` the newest backup over `/opt/uptime-kuma/data/kuma.db`, revert the compose tag to `2.3.2`, `docker compose up -d`.

- [ ] **Step 7: Reclaim the old image**

```bash
ssh aws-kuma-production 'sudo docker image prune -f; df -h / | awk "NR==2 {print \$4\" free\"}"'
```

- [ ] **Step 8: Commit the runbook note**

```bash
git -C D:/cheeko-backend commit -am "docs(monitoring): Kuma pinned to 2.5.3" --allow-empty
```

---

## Task 3: Write the monitor inventory as the source of truth

Because Kuma has no provisioning API, the repo file *is* the reproducibility mechanism. Dev and prod are both built by following it. Write it before touching the UI so the UI work is transcription, not design.

**Files:**
- Create: `D:/cheeko-backend/docs/monitoring/monitor-inventory.md`

**Interfaces:**
- Consumes: nothing.
- Produces: the canonical tier definitions and monitor list. Tasks 4, 5, 6, 7 and 12 all transcribe from this file.

- [ ] **Step 1: Create the inventory file**

```bash
mkdir -p D:/cheeko-backend/docs/monitoring
```

Write `D:/cheeko-backend/docs/monitoring/monitor-inventory.md`:

```markdown
# Monitor Inventory

Source of truth for Uptime Kuma. Kuma has no provisioning API, so monitors are created by hand
in the UI following this table. Change this file first, then the UI.

## Tiers

| Tier | Meaning | Notification object | Action expected |
|---|---|---|---|
| T1 PAGE | A child cannot talk to their toy right now | `T1-PAGE-telegram` | Drop everything |
| T2 WARN | Degraded but still serving | `T2-WARN-telegram` | Look today |
| T3 INFO | Dashboard and status page only | none attached | Look when convenient |

A monitor that would not cause anyone to act belongs in T3 or does not exist.

## Dev monitors (`otadev.cheekoai.in`)

| Name | Tier | Type | Target | Interval | Retries | Notes |
|---|---|---|---|---:|---:|---|
| DEV Manager API (public) | T2 | http | `https://otadev.cheekoai.in/toy/health` | 60 | 2 | Through Caddy: tests TLS, cert expiry, routing |
| DEV Manager API DB | T2 | http | `https://otadev.cheekoai.in/toy/health/db` | 120 | 2 | Endpoint returns 200 even when DB is unhappy; keyword-match instead |
| DEV MQTT Gateway | T2 | http | `http://64.227.170.31:8004/health` | 60 | 2 | Not proxied by Caddy; direct IP is correct here |
| DEV EMQX MQTT | T2 | port | `64.227.170.31:1883` | 60 | 2 | Device transport |
| DEV LiveKit | T2 | port | `64.227.170.31:7880` | 60 | 2 | Was disabled; re-enable |
| DEV process health | T2 | push | script-driven, see Task 7 | 300 | 1 | pm2 crash-loop detection |

## Dependency monitors (shared, not per-environment)

| Name | Tier | Type | Target | Interval | Notes |
|---|---|---|---|---:|---|
| Sarvam TTS (authenticated) | T1 | http POST | `https://api.sarvam.ai/text-to-speech` | 3600 | Real synth probe; keyword `audios`. Catches dead keys |
| LiveKit Cloud | T1 | port | `cheeko-prod-68ib8ma4.livekit.cloud:443` | 60 | Voice transport for prod |
| CloudFront CDN | T2 | port | `dsmzc13oafp54.cloudfront.net:443` | 180 | `CLOUDFRONT_DOMAIN` is set on dev |
| ElevenLabs API | T3 | http | `https://otadev.cheekoai.in/toy/health/deps/elevenlabs` | 3600 | Retired for now, returns later. Dashboard only, no alerts |
| Gemini API | T3 | http | `https://otadev.cheekoai.in/toy/health/deps/gemini` | 3600 | Only guards `founderDashboard.service.js`, not the voice path |

## Retired on dev

Removed because the integration is code-present but runtime-off (`manager-api` `.env` on
`64.227.170.31` sets none of their keys). **Verify prod's `.env` independently before removing
them there** — see Task 12.

| Name | Reason |
|---|---|
| Qdrant Cloud Port 443 | `QDRANT_*` unset on dev |
| Mem0 API Port 443 | `MEM0_*` unset on dev |
| Grafana Loki Port 443 | `LOKI_*` unset on dev; log shipping unconfirmed |
| Uptime Kuma Self Check | Kuma cannot report its own death. Replaced by an external dead-man's switch (Task 11) |
| Gemini API Port 443 | TCP connect proves nothing the authenticated probe does not |
| ElevenLabs API Port 443 | Same |

## EKS

Not monitored by Kuma. `picoclaw-livekit` is ClusterIP-only and is covered by Prometheus +
Alertmanager — see Tasks 8 and 9.
```

- [ ] **Step 2: Commit**

```bash
git -C D:/cheeko-backend add docs/monitoring/monitor-inventory.md
git -C D:/cheeko-backend commit -m "docs(monitoring): add monitor inventory as source of truth"
```

---

## Task 4: Retire dead monitors and stop the notification auto-attaching

**Status: COMPLETED 2026-08-31.** Two deviations:

- **The T1/T2 notification split was dropped before being built.** Both tiers pointed at the same
  bot and chat, so they would have been two objects doing identical things. Replaced by
  attached-vs-not-attached on the single existing notification. See the inventory for the reasoning.
- **Applied by direct SQLite write, not the UI.** Kuma has no CRUD API and logging into the UI to
  click through twelve deletions was not available to the agent. Safe here because every child table
  (`heartbeat`, `monitor_notification`, `monitor_tag`, `stat_daily/hourly/minutely`) declares
  `ON DELETE CASCADE`. Procedure: back up, stop the container, run the transaction with
  `PRAGMA foreign_keys=ON` (it defaults to OFF in the CLI — without it the cascades silently do not
  fire and you are left with orphan rows), restart, verify zero orphans.

  Creating monitors this way is a different risk profile from deleting them and is not recommended
  without verifying each new monitor actually produces correct heartbeats.


**Files:**
- No repo files. Kuma UI at `http://16.112.52.71:3001`.

**Interfaces:**
- Consumes: tier definitions from `docs/monitoring/monitor-inventory.md`.
- Produces: two notification objects named exactly `T1-PAGE-telegram` and `T2-WARN-telegram`. Tasks 5, 6 and 7 attach monitors to these by name.

- [ ] **Step 1: Read the existing Telegram config so the new objects reuse the working credentials**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 /app/data/kuma.db "select config from notification where id=1;"'
```

Note the `botToken` and `chatID`. If T1 should go to a different chat than T2, get that chat ID from the user now.

- [ ] **Step 2: Create the two tier notifications in the UI**

In Settings → Notifications, create:
- `T1-PAGE-telegram` — Telegram, same bot token, T1 chat ID. **Uncheck "Default enabled".**
- `T2-WARN-telegram` — Telegram, same bot token, T2 chat ID. **Uncheck "Default enabled".**

Leave the existing `My Telegram Alert (1)` in place for now; Step 5 removes it once every monitor is re-attached.

- [ ] **Step 3: Verify both notifications deliver**

Use the "Test" button on each. Expected: a message arrives in the corresponding Telegram chat. Do not continue until both are confirmed — a tier that does not deliver is worse than no tier.

- [ ] **Step 4: Delete the retired monitors**

Delete these, per the "Retired on dev" table in the inventory: `Qdrant Cloud Port 443` (11), `Mem0 API Port 443` (12), `Grafana Loki Port 443` (13), `Uptime Kuma Self Check` (15), `Gemini API Port 443` (17), `ElevenLabs API Port 443` (19).

Also delete `Manager API Remote Health` if present, and the DO-prod monitors 1, 2, 3, 4, 6, 7 — those are prod targets being replaced in Task 12, and leaving them means dev and prod monitors are indistinguishable in the UI during the soak.

- [ ] **Step 5: Verify what survived**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select id,name,active from monitor order by id;"'
```

Expected: only `LiveKit Production Cloud Port 443` (9), `CloudFront CDN Port 443` (14), `Gemini API Health` (16), and `ElevenLabs API Health` (18) remain. Everything else is rebuilt in Task 5.

- [ ] **Step 6: Demote the retained ElevenLabs and Gemini monitors to T3**

Edit monitors 16 and 18: detach all notifications. They stay visible on the dashboard and stop paging. This is what "retain but demote" means — the ElevenLabs 401 remains visible without training anyone to ignore Telegram.

- [ ] **Step 7: Confirm the permanently-red monitor no longer notifies**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select m.id,m.name,count(mn.notification_id) as notifs from monitor m left join monitor_notification mn on mn.monitor_id=m.id group by m.id;"'
```

Expected: monitors 16 and 18 show `0` notifications.

---

## Task 5: Rebuild dev monitors through the public hostname

Every current monitor hits a raw IP, bypassing Caddy — the component most likely to fail first, and the only one that can expire a TLS certificate.

**Files:**
- No repo files. Kuma UI.

**Interfaces:**
- Consumes: `T2-WARN-telegram` from Task 4; the "Dev monitors" table from Task 3.
- Produces: the dev blackbox monitor set.

- [ ] **Step 1: Confirm the public endpoints actually answer before monitoring them**

```bash
for p in /toy/health /toy/health/db; do echo "== $p"; curl -s -m 15 -o /dev/null -w "%{http_code} tls_expiry_ok\n" "https://otadev.cheekoai.in$p"; done
curl -s -m 10 -o /dev/null -w "gateway %{http_code}\n" http://64.227.170.31:8004/health
```

Expected: `200` for each. If `/toy/health` does not answer through Caddy, the Caddyfile route differs from the assumption in Task 3 — fix the inventory table and this step before creating monitors.

- [ ] **Step 2: Create the five dev monitors in the UI**

Transcribe the "Dev monitors" table from `docs/monitoring/monitor-inventory.md`. For each: set the interval and retries from the table, and attach **only** `T2-WARN-telegram`.

Leave the `DEV process health` push monitor for Task 7.

- [ ] **Step 3: Add a keyword condition to the DB monitor**

`/toy/health/db` returns HTTP 200 even when the database is unhealthy — a status-code check on it is meaningless. Confirm the healthy response body, then match on it:

```bash
curl -s -m 15 https://otadev.cheekoai.in/toy/health/db
```

Set the monitor type to **HTTP(s) - Keyword** with the keyword matching the healthy marker in that body (for example `"status":"healthy"`). Verify by reading the actual output rather than assuming the shape.

- [ ] **Step 4: Verify all five go green**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select m.name,h.status,datetime(h.time),substr(h.msg,1,40) from monitor m join heartbeat h on h.id=(select id from heartbeat where monitor_id=m.id order by time desc limit 1) where m.name like \"DEV%\";"'
```

Expected: every `DEV *` monitor at status `1`.

- [ ] **Step 5: Verify TLS expiry is being tracked**

Open the `DEV Manager API (public)` monitor in the UI. Expected: a certificate expiry countdown is shown. This is the capability the old raw-IP monitors could not provide.

---

## Task 6: Replace port-443 theatre with an authenticated Sarvam probe

A TCP connect to `:443` is what let a revoked ElevenLabs key hide for twelve days. Sarvam is now on the voice path and deserves a probe that would actually catch the same failure.

**Files:**
- No repo files. Kuma UI.

**Interfaces:**
- Consumes: `T1-PAGE-telegram` from Task 4; `SARVAM_API_KEY` from the user.
- Produces: a `Sarvam TTS (authenticated)` monitor that fails on auth or quota errors.

- [ ] **Step 1: Confirm Task 1's lockdown is in force**

```bash
aws ec2 describe-security-groups --region ap-south-2 --group-ids sg-0ca72b10de2f2e764 --query 'SecurityGroups[0].IpPermissions[].IpRanges[].CidrIp' --output text
```

Expected: only `${ADMIN_CIDR}`. **If `0.0.0.0/0` appears, stop.** This step stores an API key in an unencrypted SQLite file; it must not be publicly reachable.

- [ ] **Step 2: Verify the probe request works from the command line first**

```bash
curl -s -m 30 -X POST "https://api.sarvam.ai/text-to-speech" \
  -H "api-subscription-key: $SARVAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"text":"ok","model":"bulbul:v3","speaker":"pooja","target_language_code":"hi-IN","speech_sample_rate":8000}' \
  | head -c 120
```

Expected: a JSON body beginning with `{"audios":[`. If it returns 401 or 403, the key is wrong — fix that before building a monitor around it.

- [ ] **Step 3: Create the monitor**

In the UI, add an **HTTP(s) - Keyword** monitor:
- Name: `Sarvam TTS (authenticated)`
- URL: `https://api.sarvam.ai/text-to-speech`
- Method: `POST`
- Headers: `{"api-subscription-key": "<key>", "Content-Type": "application/json"}`
- Body: the JSON from Step 2
- Keyword: `audios`
- Interval: `3600`, Retries: `2`
- Notification: `T1-PAGE-telegram`

Interval is one hour deliberately: each check performs a real synthesis and costs money. `speech_sample_rate` is set to the minimum and the text to two characters to keep that cost negligible.

- [ ] **Step 4: Verify it goes green**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -column /app/data/kuma.db "select m.name,h.status,substr(h.msg,1,50) from monitor m join heartbeat h on h.id=(select id from heartbeat where monitor_id=m.id order by time desc limit 1) where m.name like \"Sarvam%\";"'
```

Expected: status `1`.

- [ ] **Step 5: Prove it detects a bad key — the whole point of this task**

Temporarily edit the monitor's header to an invalid key, save, use "Resume"/manual re-check, and confirm the monitor goes red and Telegram fires. Then restore the correct key and confirm it returns green.

Expected: red on the bad key. If it stays green, the keyword match is wrong and the monitor is theatre — fix it before moving on.

---

## Task 7: Dev box process health via Kuma push

`manager-api` restarted 699 times in 3 hours while every HTTP check stayed green, because pm2 restarts faster than a probe interval. The observer must sit outside the process.

**Files:**
- Create: `/opt/kuma/pm2-health.sh` (on `64.227.170.31`)
- Create: `/etc/cron.d/kuma-pm2-health` (on `64.227.170.31`)
- Create: `D:/cheeko-backend/docs/monitoring/pm2-health.sh` (checked-in copy)

**Interfaces:**
- Consumes: `T2-WARN-telegram`.
- Produces: a push monitor named `DEV process health` whose token is consumed by the script.

- [ ] **Step 1: Create the push monitor and capture its token**

In the UI, add a **Push** monitor:
- Name: `DEV process health`
- Heartbeat Interval: `300`, Retries: `1`
- Notification: `T2-WARN-telegram`

Copy the push URL Kuma displays. Extract the token:

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -column /app/data/kuma.db "select id,name,push_token from monitor where type=\"push\";"'
```

- [ ] **Step 2: Confirm the dev box can reach Kuma**

The Task 1 lockdown restricted port 3001 to `${ADMIN_CIDR}`. The dev box is a different source IP and will be blocked.

```bash
ssh 64.227.170.31 'curl -s -o /dev/null -w "%{http_code}\n" -m 10 http://16.112.52.71:3001'
```

If this is not `200`/`302`, allow the dev box explicitly:

```bash
aws ec2 authorize-security-group-ingress --region ap-south-2 --group-id sg-0ca72b10de2f2e764 --ip-permissions "IpProtocol=tcp,FromPort=3001,ToPort=3001,IpRanges=[{CidrIp=64.227.170.31/32,Description=dev-box-push}]"
```

Re-run the curl and confirm it succeeds before continuing.

- [ ] **Step 3: Write the health script**

Write `D:/cheeko-backend/docs/monitoring/pm2-health.sh`:

```bash
#!/usr/bin/env bash
# Reports pm2 restart-rate health to an Uptime Kuma push monitor.
# ponytail: alerting logic lives in shell because these droplets have no Prometheus.
# Ceiling: no history, no rate-of-change queries, no versioned rules. If the DO boxes
# become long-lived production, replace this with Prometheus, do not extend it.
set -uo pipefail

KUMA_URL="${KUMA_URL:?set KUMA_URL}"     # e.g. http://16.112.52.71:3001
PUSH_TOKEN="${PUSH_TOKEN:?set PUSH_TOKEN}"
STATE=/var/lib/kuma-pm2-state.json
# Services that must be up. Others (line-art, visitors-register) are not on the voice path.
WATCH="manager-api manager-web picoclaw-livekit gw-0 gw-1 gw-2 gw-3"
# More than this many restarts between runs means crash-looping, not a one-off restart.
THRESHOLD=3

now=$(pm2 jlist 2>/dev/null) || { curl -fsS --max-time 10 "$KUMA_URL/api/push/$PUSH_TOKEN?status=down&msg=pm2%20jlist%20failed" >/dev/null; exit 0; }

prev="{}"
[ -f "$STATE" ] && prev=$(cat "$STATE")

problems=""
snapshot="{}"

for svc in $WATCH; do
  line=$(echo "$now" | jq -r --arg n "$svc" '.[] | select(.name==$n) | "\(.pm2_env.status) \(.pm2_env.restart_time)"')
  if [ -z "$line" ]; then
    problems="$problems $svc:missing"
    continue
  fi
  status=${line% *}
  restarts=${line#* }
  snapshot=$(echo "$snapshot" | jq --arg n "$svc" --argjson r "$restarts" '.[$n]=$r')

  if [ "$status" != "online" ]; then
    problems="$problems $svc:$status"
    continue
  fi

  was=$(echo "$prev" | jq -r --arg n "$svc" '.[$n] // empty')
  if [ -n "$was" ]; then
    delta=$((restarts - was))
    # A pm2 resurrect resets the counter; a negative delta is not a fault.
    if [ "$delta" -gt "$THRESHOLD" ]; then
      problems="$problems $svc:${delta}restarts"
    fi
  fi
done

echo "$snapshot" > "$STATE"

if [ -n "$problems" ]; then
  curl -fsS --max-time 10 -G "$KUMA_URL/api/push/$PUSH_TOKEN" \
    --data-urlencode "status=down" \
    --data-urlencode "msg=unhealthy:$problems" >/dev/null
else
  curl -fsS --max-time 10 -G "$KUMA_URL/api/push/$PUSH_TOKEN" \
    --data-urlencode "status=up" \
    --data-urlencode "msg=all $WATCH healthy" >/dev/null
fi
```

- [ ] **Step 4: Verify `jq` exists on the dev box, then install the script**

```bash
ssh 64.227.170.31 'which jq || apt-get install -y jq'
scp D:/cheeko-backend/docs/monitoring/pm2-health.sh 64.227.170.31:/opt/kuma/pm2-health.sh
ssh 64.227.170.31 'mkdir -p /opt/kuma && chmod +x /opt/kuma/pm2-health.sh'
```

- [ ] **Step 5: Run it once by hand and confirm Kuma receives the beat**

```bash
ssh 64.227.170.31 'KUMA_URL=http://16.112.52.71:3001 PUSH_TOKEN=<token> /opt/kuma/pm2-health.sh; echo exit=$?; cat /var/lib/kuma-pm2-state.json'
```

Then confirm the beat landed:

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -column /app/data/kuma.db "select m.name,h.status,datetime(h.time),substr(h.msg,1,60) from monitor m join heartbeat h on h.monitor_id=m.id where m.type=\"push\" order by h.time desc limit 3;"'
```

Expected: a heartbeat from seconds ago. Given the current 699-restart situation, it will most likely be `status=0` with a message naming the crash-looping services — **that is the correct result**, and it is the first time that failure has ever been visible.

- [ ] **Step 6: Prove the detection works deliberately**

```bash
ssh 64.227.170.31 'pm2 stop manager-web'
ssh 64.227.170.31 'KUMA_URL=http://16.112.52.71:3001 PUSH_TOKEN=<token> /opt/kuma/pm2-health.sh'
```

Expected: the monitor reports down with `manager-web:stopped`, and Telegram fires. Then:

```bash
ssh 64.227.170.31 'pm2 start manager-web'
```

- [ ] **Step 7: Schedule it every 2 minutes**

The monitor's heartbeat interval is 300s, so a missed run is itself an alert — the dead-man's-switch property. Running every 2 minutes gives two chances before Kuma declares it down.

```bash
ssh 64.227.170.31 'cat > /etc/cron.d/kuma-pm2-health <<EOF
KUMA_URL=http://16.112.52.71:3001
PUSH_TOKEN=<token>
*/2 * * * * root /opt/kuma/pm2-health.sh >> /var/log/kuma-pm2-health.log 2>&1
EOF
chmod 644 /etc/cron.d/kuma-pm2-health'
```

- [ ] **Step 8: Confirm beats keep arriving unattended**

Wait five minutes, then:

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -column /app/data/kuma.db "select count(*),datetime(max(time)) from heartbeat where monitor_id=(select id from monitor where type=\"push\");"'
```

Expected: at least two beats, the newest within two minutes.

- [ ] **Step 9: Commit the script**

```bash
git -C D:/cheeko-backend add docs/monitoring/pm2-health.sh
git -C D:/cheeko-backend commit -m "feat(monitoring): pm2 crash-loop detection via Kuma push"
```

- [ ] **Step 10: File the stability defect as separate work**

The alerts from this task will be noisy until the crash loops are fixed, and a permanently-firing alert gets muted — the exact failure that killed the ElevenLabs monitor. Open an issue titled "Fix manager-api and picoclaw-livekit crash loops on dev box" recording: `manager-api` 699 restarts / 3h, `picoclaw-livekit` 129 restarts / 65m, `admin-dashboard` 70, `line-art` 67, all observed 2026-08-31. **This is a prerequisite for the Task 7 alerts being useful**, not a follow-up nicety.

---

## Task 8: Give Prometheus durable storage

`--storage.tsdb.retention.time=15d` is configured, but storage is an `emptyDir` with no PVC — every pod restart silently wipes all history. Trend alerting on a self-emptying database is worse than none, because it is believed.

**Files:**
- Create: `deploy/k8s/monitoring/prometheus-values.yaml` (in `D:/picoclaw`)

**Interfaces:**
- Consumes: nothing.
- Produces: a Prometheus with a PVC. Task 9's rules depend on history surviving restarts.

- [ ] **Step 1: Install Helm and confirm the release**

The chart was installed with Helm (`app.kubernetes.io/managed-by: Helm`), but `helm` is not on this machine. Patching with `kubectl` would drift from the release state and be reverted by the next `helm upgrade`.

```bash
winget install Helm.Helm
helm list -n monitoring
```

Expected: a release named `prometheus`, chart `prometheus-29.7.0`. If Helm reports no release, stop — the chart may have been installed with `--dry-run > manifest.yaml | kubectl apply`, which changes the whole approach for this task and Task 9.

- [ ] **Step 2: Capture current values before changing anything**

```bash
helm get values prometheus -n monitoring > D:/picoclaw/deploy/k8s/monitoring/prometheus-values-current.yaml
cat D:/picoclaw/deploy/k8s/monitoring/prometheus-values-current.yaml
```

- [ ] **Step 3: Confirm a default StorageClass exists**

```bash
kubectl get storageclass
```

Expected: one marked `(default)` — normally `gp2` or `gp3` on EKS. If none is default, the PVC will stay `Pending` and Prometheus will not start. Name it explicitly in Step 4 if so.

- [ ] **Step 4: Write the values overlay**

Write `D:/picoclaw/deploy/k8s/monitoring/prometheus-values.yaml`:

```yaml
# Overlay for the existing `prometheus` release in ns `monitoring`.
# Apply with: helm upgrade prometheus prometheus-community/prometheus \
#   -n monitoring --reuse-values -f prometheus-values.yaml
#
# Storage was emptyDir, so every pod restart wiped all metrics history. 10Gi at a 1m
# scrape interval comfortably holds the configured 15d retention for this workload.
server:
  persistentVolume:
    enabled: true
    size: 10Gi
  retention: "15d"

# Alertmanager is enabled in Task 9, not here — one change at a time.
```

- [ ] **Step 5: Apply and watch the rollout**

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade prometheus prometheus-community/prometheus -n monitoring --reuse-values -f D:/picoclaw/deploy/k8s/monitoring/prometheus-values.yaml --version 29.7.0
kubectl -n monitoring rollout status deploy/prometheus-server --timeout=300s
```

Pinning `--version 29.7.0` keeps this change to storage alone. Upgrading the chart is a separate decision.

- [ ] **Step 6: Verify the PVC is bound and mounted**

```bash
kubectl -n monitoring get pvc
kubectl -n monitoring get deploy prometheus-server -o jsonpath='{.spec.template.spec.volumes}' | tr ',' '\n' | grep -i claim
```

Expected: a `Bound` PVC of 10Gi, and `claimName` where `emptyDir` used to be.

- [ ] **Step 7: Verify the HPA still gets its custom metric**

This is the regression that matters — Prometheus feeds your autoscaler, and history was just reset by the restart.

```bash
kubectl -n picoclaw-dev get hpa picoclaw-livekit
```

Expected: the targets column shows a number (such as `0/60`), **not** `<unknown>`. Metrics take a few minutes to repopulate after the restart; if it still reads `<unknown>` after five minutes, check `kubectl -n monitoring logs deploy/prometheus-adapter`.

- [ ] **Step 8: Prove history now survives a restart**

```bash
kubectl -n monitoring delete pod -l app.kubernetes.io/component=server
kubectl -n monitoring rollout status deploy/prometheus-server --timeout=300s
kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- \
  wget -qO- 'http://localhost:9090/api/v1/query?query=count_over_time(up[1h])' | head -c 300
```

Expected: non-zero counts, proving data predates the restart. Under the old `emptyDir` this would have returned nothing.

- [ ] **Step 9: Commit**

```bash
git -C D:/picoclaw add deploy/k8s/monitoring/prometheus-values.yaml
git -C D:/picoclaw commit -m "feat(monitoring): give Prometheus a PVC so history survives restarts"
```

---

## Task 9: Deploy Alertmanager and EKS alert rules

**Files:**
- Modify: `deploy/k8s/monitoring/prometheus-values.yaml`

**Interfaces:**
- Consumes: the durable Prometheus from Task 8.
- Produces: Alertmanager notifying the same Telegram chat as Kuma's T1 tier.

- [ ] **Step 1: Confirm the metrics the rules depend on actually exist**

There is no `kube-state-metrics` and no `node-exporter`, so `kube_*` and `node_*` metrics are unavailable. Verify what is present before writing rules against it:

```bash
kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- sh -c '
for q in "container_start_time_seconds{namespace=\"picoclaw-dev\"}" "picoclaw_livekit_session_load_percent" "up{job=\"kubernetes-pods\"}"; do
  echo "== $q"
  wget -qO- "http://localhost:9090/api/v1/query?query=$(echo $q | sed "s/ /%20/g")" | head -c 200; echo
done'
```

Expected: each returns a non-empty `result` array. **Any query returning `[]` means its rule below will never fire** — fix the query before shipping the rule.

- [ ] **Step 2: Add Alertmanager and rules to the values overlay**

Append to `D:/picoclaw/deploy/k8s/monitoring/prometheus-values.yaml`:

```yaml
alertmanager:
  enabled: true
  persistence:
    enabled: false          # silences only; losing them on restart is acceptable
  config:
    route:
      receiver: telegram
      group_by: ['alertname']
      group_wait: 30s
      group_interval: 5m
      repeat_interval: 4h   # long enough not to train anyone to mute it
    receivers:
      - name: telegram
        telegram_configs:
          - bot_token: '<same bot token as Kuma>'
            chat_id: <T1 chat id>
            parse_mode: ''
            message: '{{ .CommonLabels.severity }} {{ .CommonLabels.alertname }} — {{ .CommonAnnotations.summary }}'

serverFiles:
  alerting_rules.yml:
    groups:
      - name: picoclaw-livekit
        rules:
          # Restart detection without kube-state-metrics: cadvisor container start times.
          - alert: PicoclawLivekitCrashLooping
            expr: changes(container_start_time_seconds{namespace="picoclaw-dev",container="picoclaw-livekit"}[30m]) > 2
            for: 5m
            labels:
              severity: T1
            annotations:
              summary: "picoclaw-livekit restarted more than twice in 30m"

          # Below the HPA's minReplicas of 2.
          - alert: PicoclawLivekitBelowMinReplicas
            expr: count(up{job="kubernetes-pods",namespace="picoclaw-dev"} == 1) < 2
            for: 5m
            labels:
              severity: T1
            annotations:
              summary: "Fewer than 2 picoclaw-livekit pods are up"

          # Per-pod ceiling is 15 sessions; HPA targets 60%. Sustained 85% means
          # scaling is not keeping up.
          - alert: PicoclawLivekitSessionSaturation
            expr: avg(picoclaw_livekit_session_load_percent) > 85
            for: 10m
            labels:
              severity: T2
            annotations:
              summary: "Session load above 85% for 10m — HPA may not be keeping up"

          # Scrape loss also breaks the HPA's custom metric, so this is not cosmetic.
          - alert: PicoclawLivekitScrapeTargetMissing
            expr: absent(picoclaw_livekit_session_load_percent)
            for: 10m
            labels:
              severity: T2
            annotations:
              summary: "Prometheus lost the picoclaw-livekit scrape target — HPA autoscaling is degraded"
```

- [ ] **Step 3: Apply**

```bash
helm upgrade prometheus prometheus-community/prometheus -n monitoring --reuse-values -f D:/picoclaw/deploy/k8s/monitoring/prometheus-values.yaml --version 29.7.0
kubectl -n monitoring rollout status deploy/prometheus-alertmanager --timeout=300s
```

- [ ] **Step 4: Verify the rules loaded without syntax errors**

```bash
kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- \
  wget -qO- http://localhost:9090/api/v1/rules | head -c 600
```

Expected: the four alert names appear. A malformed rule file causes Prometheus to skip it silently — an empty result means the rules are not active.

- [ ] **Step 5: Verify Prometheus is talking to Alertmanager**

```bash
kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- \
  wget -qO- http://localhost:9090/api/v1/alertmanagers
```

Expected: `activeAlertmanagers` contains one entry. If empty, the chart did not wire the `alerting:` block — check `helm get values`.

- [ ] **Step 6: Prove an alert reaches Telegram end to end**

Fire a real one rather than trusting configuration. Scale to one replica to trip `PicoclawLivekitBelowMinReplicas`:

```bash
kubectl -n picoclaw-dev scale deploy/picoclaw-livekit --replicas=1
```

Wait 6 minutes (5m `for` plus evaluation). Expected: a Telegram message naming `PicoclawLivekitBelowMinReplicas`. Then restore:

```bash
kubectl -n picoclaw-dev scale deploy/picoclaw-livekit --replicas=2
kubectl -n picoclaw-dev get hpa picoclaw-livekit
```

Expected: 2 replicas, HPA reporting normally. **If no Telegram message arrived, the alerting path is broken and every rule in this task is decoration** — do not proceed until it delivers.

- [ ] **Step 7: Commit**

```bash
git -C D:/picoclaw add deploy/k8s/monitoring/prometheus-values.yaml
git -C D:/picoclaw commit -m "feat(monitoring): Alertmanager plus EKS alert rules for picoclaw-livekit"
```

---

## Task 10: Status page

**Files:**
- Modify: `D:/cheeko-backend/docs/monitoring/monitor-inventory.md`

**Interfaces:**
- Consumes: the monitors from Tasks 5, 6, 7.
- Produces: a status page URL.

- [ ] **Step 1: Confirm the audience with the user**

Ask whether the status page is team-facing or parent-facing. This decides whether it is published and whether internal monitor names are exposed. **Do not publish a public page without an explicit yes** — it makes internal service names and outage history externally visible.

- [ ] **Step 2: Create the page in the UI**

Status Pages → New. Add the `DEV *` monitors and the dependency monitors. Use plain-language display names ("Voice service", not `picoclaw-livekit`) if the audience is anyone but the team.

- [ ] **Step 3: Verify it renders**

```bash
curl -s -m 10 -o /dev/null -w "%{http_code}\n" http://16.112.52.71:3001/status/<slug>
```

Expected: `200`. Note that the page is only reachable from `${ADMIN_CIDR}` after Task 1 — if it needs a wider audience, that is a network change to decide deliberately, not to bolt on here.

- [ ] **Step 4: Record the URL in the inventory and commit**

```bash
git -C D:/cheeko-backend commit -am "docs(monitoring): record status page URL"
```

---

## Task 11: Add an external dead-man's switch and soak

Kuma cannot report its own death, which is why the old `Uptime Kuma Self Check` monitor was deleted rather than retargeted. Something outside Kuma must watch Kuma.

**Files:**
- Create: `/etc/cron.d/kuma-deadman` (on `aws-kuma-production`)

**Interfaces:**
- Consumes: a free healthchecks.io (or equivalent) check URL.
- Produces: an alert when Kuma itself stops.

- [ ] **Step 1: Create a free external check**

Create a check at healthchecks.io (free tier) with a 5-minute period and 5-minute grace, notifying the same Telegram chat. Copy its ping URL.

- [ ] **Step 2: Ping it from the Kuma host, gated on Kuma actually being healthy**

```bash
ssh aws-kuma-production 'echo "*/3 * * * * root curl -fsS -m 10 http://127.0.0.1:3001 > /dev/null && curl -fsS -m 10 <healthchecks-url> > /dev/null" | sudo tee /etc/cron.d/kuma-deadman && sudo chmod 644 /etc/cron.d/kuma-deadman'
```

The `&&` matters: it pings only when Kuma answers, so a hung Kuma on a healthy host still trips the switch.

- [ ] **Step 3: Verify it registers**

Wait 4 minutes, then confirm healthchecks.io shows the check as up with a recent ping.

- [ ] **Step 4: Prove it fires**

```bash
ssh aws-kuma-production 'sudo docker stop uptime-kuma'
```

Wait 10 minutes. Expected: a Telegram alert from healthchecks.io. Then:

```bash
ssh aws-kuma-production 'sudo docker start uptime-kuma; sleep 20; sudo docker ps --filter name=uptime-kuma --format "{{.Status}}"'
```

- [ ] **Step 5: Soak for one week and count alerts**

Run for seven days without changing thresholds. Then:

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select m.name, count(*) as alerts from heartbeat h join monitor m on m.id=h.monitor_id where h.important=1 and h.time > datetime(\"now\",\"-7 days\") group by m.name order by alerts desc;"'
```

- [ ] **Step 6: Act on the soak result**

Apply the rule from the spec: **every alert is actionable, or it is deleted.** For each monitor firing more than roughly 3 times a week, either fix the underlying cause or demote it to T3. A monitor that cried wolf all week and was ignored has already failed, and carrying it into prod in Task 12 propagates the failure.

Record the decisions in `docs/monitoring/monitor-inventory.md` and commit.

---

## Task 12: Replicate to prod

Only after the dev set has soaked and been tuned.

**Files:**
- Modify: `D:/cheeko-backend/docs/monitoring/monitor-inventory.md`

**Interfaces:**
- Consumes: the tuned dev configuration.
- Produces: the prod monitor set.

- [ ] **Step 1: Check prod's environment independently — do not assume it matches dev**

The dev box has none of `MEM0_*`, `QDRANT_*`, `LOKI_*` set, which is why those monitors were retired there. Prod may differ, and retiring a live integration's monitor is exactly the blind spot this project exists to remove.

```bash
ssh 139.59.7.72 'f=$(find / -maxdepth 6 -path /proc -prune -o -name ".env" -path "*manager-api*" -print 2>/dev/null | head -1); echo "envfile=$f"; grep -icE "^(MEM0|QDRANT|LOKI|SARVAM|ELEVENLABS)[A-Z_]*=" "$f"; grep -oE "^(MEM0|QDRANT|LOKI|SARVAM|ELEVENLABS)[A-Z_]*=" "$f"'
```

Record which are set. Any that are set stay monitored on prod.

- [ ] **Step 2: Identify prod's public hostname**

```bash
ssh 139.59.7.72 'cat /etc/caddy/Caddyfile 2>/dev/null | grep -E "^[a-z0-9.-]+ \{" | head'
```

Prod monitors go through this hostname, not `139.59.7.72`, for the same reason dev does.

- [ ] **Step 3: Add the prod section to the inventory**

Mirror the dev table in `docs/monitoring/monitor-inventory.md` with prod's hostname, plus any integrations Step 1 showed to be live. Prod monitors are **T1** where dev's equivalents were T2 — a prod outage means real children cannot use their toys.

- [ ] **Step 4: Create the prod monitors in the UI**

Transcribe the prod table. Prefix every name with `PROD ` so the two environments are never confused in an alert message.

- [ ] **Step 5: Install the pm2 health script on the prod box**

Repeat Task 7 Steps 1–8 against `139.59.7.72`: create a `PROD process health` push monitor, allow the prod box's IP to reach port 3001, install the same script, and verify with a deliberate `pm2 stop` of a non-critical service.

Use a **non-critical** service for the test — `visitors-register`, not `manager-api`. This is production.

- [ ] **Step 6: Verify the full prod set is green**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select m.name,h.status,datetime(h.time) from monitor m join heartbeat h on h.id=(select id from heartbeat where monitor_id=m.id order by time desc limit 1) where m.name like \"PROD%\";"'
```

Expected: every `PROD *` monitor at status `1`. Investigate any that are not — a monitor that is red on the day it is created is either misconfigured or has found a real problem, and both need resolving now.

- [ ] **Step 7: Final state check against the spec's success criteria**

```bash
ssh aws-kuma-production 'sudo docker exec uptime-kuma sqlite3 -header -column /app/data/kuma.db "select m.name,h.status from monitor m join heartbeat h on h.id=(select id from heartbeat where monitor_id=m.id order by time desc limit 1) where h.status=0;"'
```

Expected: empty, or containing only the deliberately-demoted T3 ElevenLabs monitor. Any other permanently-red monitor violates the core principle and must be fixed or deleted.

- [ ] **Step 8: Commit**

```bash
git -C D:/cheeko-backend add docs/monitoring/monitor-inventory.md
git -C D:/cheeko-backend commit -m "docs(monitoring): add prod monitor set"
```

---

## Self-Review Notes

**Spec coverage:** Layer 1 (Kuma blackbox) → Tasks 4, 5, 6. Layer 2 (Prometheus/Alertmanager) → Tasks 8, 9. Layer 3 (push) → Task 7. Hardening prerequisite → Task 1. Upgrade → Task 2. Tiers → Tasks 3, 4. Status page → Task 10. Dead-man's switch → Task 11 (spec listed it under retired monitors; given its own task since Kuma cannot self-check). Soak/tune → Task 11. Prod rollout → Task 12. Crash loops declared out of scope → filed in Task 7 Step 10.

**Open questions from the spec, resolved or carried:**
- `ADMIN_CIDR` — a Global Constraint, required before Task 1.
- Telegram chat per tier — resolved in Task 4 Step 1.
- Status page audience — resolved in Task 10 Step 1.

**Known ceiling:** Task 7's shell-based alerting is a deliberate shortcut, marked with a `ponytail:` comment naming its upgrade path. Monitor provisioning is manual because Kuma exposes no CRUD API and `uptime-kuma-api` does not support 2.x; the inventory file is the reproducibility mechanism.
