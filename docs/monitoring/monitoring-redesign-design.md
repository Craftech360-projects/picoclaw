# Monitoring Redesign — Design

Date: 2026-08-31
Status: Draft, pending review
Scope: Cheeko monitoring across DO dev box, DO prod box, EKS prod, third-party dependencies

## Problem

The current Uptime Kuma setup produces alerts nobody acts on and misses the failures that matter.

Evidence gathered 2026-08-31:

- `ElevenLabs API Health` red since 2026-08-19 (12 days, 114 failed beats). Telegram fired; ignored.
- Dev box process health is invisible to every HTTP monitor — nothing watches pm2 at all.
  (An early reading of "699 restarts in 3 hours" was a **misreading**: pm2's `restart_time` is a
  cumulative lifetime counter, and its `uptime` column is time since the *last* restart, not a
  measurement window. Re-measured 2026-08-31: `manager-api` 699 lifetime restarts with **363 minutes
  of continuous uptime**, `picoclaw-livekit` 129 lifetime with 193 minutes up. Both stable. The
  monitoring gap is real; the crisis was not.)
- EKS prod (`picoclaw-dev/picoclaw-livekit`, 2 pods) has zero monitors.
- 6 of 16 monitors are TCP `:443` connect checks that cannot detect auth or quota failure.
- All monitors target raw IPs, bypassing `otadev.cheekoai.in` / Caddy — the path devices actually use.
- Kuma 2.3.2 (latest 2.5.3). Port 3001 and SSH open to `0.0.0.0/0`, plain HTTP, no backups.

Root cause of the ignored alert is not tooling. It is that the monitor set contains checks that are
not actionable, which trains the reader to ignore all of them.

## Principles

1. **Every alert is actionable.** A check nobody would act on is deleted or demoted to dashboard-only.
2. **The observer sits outside the observed.** A health endpoint inside a crash-looping process
   cannot report its own crash loop.
3. **One destination.** Many producers may alert, but they converge on one channel that gets read.
4. **Use what is already installed** before adding anything.

## Architecture

Three layers, each matched to what it can actually see.

| Layer | Question answered | Tool | Vantage point |
|---|---|---|---|
| Blackbox | Can a device reach the service? | Uptime Kuma | Outside all infra (EC2 `16.112.52.71`) |
| Metrics | Is the system decaying internally? | Prometheus + Alertmanager | Inside EKS |
| Process | Are the DO box services staying up? | Kuma push + cron script | On-box, outside the process |

Notification: all three converge on one Telegram channel today, with severity encoded in the
message. Upgradeable to Grafana OnCall or PagerDuty later without touching the producers.

### Layer 1 — Kuma (blackbox + status page)

Kuma gets *smaller* and sharper. It answers exactly one question well: is the front door open.

Monitor tiers, implemented as three Kuma notification objects with per-monitor attachment:

- **T1 PAGE** — "a child cannot talk to their toy right now." Loud, no quiet hours.
- **T2 WARN** — degraded but serving. Notified, not urgent.
- **T3 INFO** — dashboard and status page only, no notification.

Kuma probes go through the public hostname (`otadev.cheekoai.in` on dev), never raw IPs, so the
check exercises Caddy, TLS, cert expiry, and routing — the layers that fail first.

Paid dependencies get real authenticated probes (HTTP POST with API key, keyword match on the
response body), not TCP `:443` connects. Sarvam is the priority; ElevenLabs is retained in a
demoted state because it will be used again.

### Layer 2 — Prometheus + Alertmanager (EKS decay)

Already installed and load-bearing: `prometheus-server` v3.11.3 (chart `prometheus-29.7.0`) scrapes
`picoclaw-livekit` pods on `:8192/metrics`, and `prometheus-adapter` republishes
`picoclaw_livekit_session_load_percent` for the HPA. If it stops, session-based autoscaling
silently degrades to CPU-only.

Two blockers before it can carry alerting:

- **Alertmanager is not deployed.** Chart installed with it disabled. Rule files are wired
  (`alerting_rules.yml`, `/etc/config/alerts`) and empty.
- **Storage is `emptyDir`, no PVC.** `--storage.tsdb.retention.time=15d` is configured but inert;
  every pod restart wipes history. Trend alerting on a self-emptying database is worse than none,
  because it is trusted.

Both must be fixed before any rule is written. Neither is large.

Alert rules cover what only an in-cluster view can see: pod restart rate, replica availability below
HPA `minReplicas`, session saturation approaching the per-pod ceiling of 15, and scrape target loss
(which also protects the autoscaler).

### Layer 3 — Kuma push (DO box process health)

The DO boxes have no Prometheus and will not get one. A cron script per box reads pm2 state,
evaluates a threshold itself, and reports its verdict to a Kuma push monitor:

```
GET /api/push/<token>?status=up|down&msg=<detail>
```

Verified against the running source (`/app/server/routers/api-router.js:47`): push monitors accept
`status`, `msg`, and `ping`, and route through the same `determineStatus` → `maxretries` →
notification path as any other monitor, including maintenance windows.

This is a deliberate shortcut with a known ceiling. Alerting logic lives in shell scripts, with no
history, no rate-of-change queries, and no version-controlled rules. It is chosen because standing up
Prometheus + node_exporter on two droplets — one of which is a dev box slated for retirement — costs
more than it returns. If the DO boxes become long-lived production, this layer should be replaced by
Prometheus, not extended.

## Out of scope, tracked separately

An earlier draft treated the pm2 restart counts as an active crash loop and made "fix the crash loops"
a prerequisite for this work. **Re-measurement withdrew that item**: the counters are cumulative and
both services are currently stable.

What survives the correction is the design consequence — a cumulative counter says nothing about
current health, so Layer 3 alerts on the **delta between snapshots**, never the raw value. A monitor
built on the raw number would have fired permanently and been muted within a week, which is the exact
failure mode that killed the ElevenLabs monitor.

## Rollout

Dev first, validated, then prod — per user direction.

Prerequisite for everything: Kuma hardening. API keys for the dependency probes land in `kuma.db`, an
unencrypted SQLite file on a host with port 3001 open to `0.0.0.0/0` over plain HTTP. Hardening is not
a cleanup task at the end; it gates Layer 1.

Order:

1. Harden and back up Kuma (SG lockdown, backup job, upgrade 2.3.2 → 2.5.3)
2. Rebuild Kuma monitor set for dev, tiered, via `otadev.cheekoai.in`
3. Layer 3 push monitors for the dev box
4. Prometheus PVC + Alertmanager + rules (EKS is prod-only)
5. Status page
6. Soak, tune thresholds against real alert volume
7. Repeat 2–3 for prod, after confirming prod env differs from dev (mem0/qdrant/loki are unconfigured
   on dev but may be live on prod)

## Decisions taken

- **Kuma stays** as the blackbox and status-page tool. It is good at that job.
- **Prometheus is used, not introduced.** Its operational cost is already paid.
- **Alertmanager and Kuma both notify one channel** rather than Alertmanager routing through Kuma.
  Multiple producers into one destination is the standard shape; forcing Alertmanager through Kuma
  push monitors would discard grouping and inhibition for no gain.
- **No new health endpoints in manager-api.** An external probe reaches the same dependencies with
  equal fidelity, no deploy, and no blindness to the host process crashing.
- **ElevenLabs monitors retained**, demoted to T3, since the provider returns later.
- **mem0 / qdrant / loki monitors retired on dev only.** They are code-present but runtime-off there
  (`.env` has none of their keys). Prod must be checked independently before retiring them.

## Success criteria

- Zero permanently-red monitors. A red monitor means someone acts, or the monitor is deleted.
- A killed `manager-api` on the dev box produces an alert within 5 minutes.
- A revoked Sarvam key produces an alert within one probe interval, not 12 days.
- Prometheus history survives a pod restart.
- Kuma survives instance loss (restorable from backup).
- Kuma is not reachable from the public internet.

## Open questions

- Which CIDR should Kuma's 3001/22 be restricted to?
- Is there an existing Telegram chat for T1 alerts, or should tiers use separate chats?
- Should the status page be public (parents) or private (team)?
