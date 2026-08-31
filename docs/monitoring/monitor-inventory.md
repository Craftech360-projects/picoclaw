# Monitor Inventory

Source of truth for Uptime Kuma. **Kuma has no provisioning API** — only `/api/push/:token`,
`/api/badge/*`, `/api/entry-page`, and the status-page router exist; monitor create/edit/delete
happens over an undocumented socket.io interface. So monitors are created **by hand in the UI**
following this file. `uptime-kuma-api` on PyPI is v1-only and unmaintained since 2023; it does not
work against 2.x.

Change this file first, then the UI. Dev and prod are both built by transcribing it.

Kuma UI: `http://16.112.52.71:3001` (admin CIDR only — see `README_UPTIME_KUMA.md` in
cheeko-backend for the recovery path when your IP changes).

## Tiers

There is exactly **one** notification object, `My Telegram Alert (1)` (Telegram, chat `445265582`),
and monitors either have it attached or they do not.

| Tier | Meaning | Notification | Action expected |
|---|---|---|---|
| ALERT | Something is broken and someone should look | attached | Act |
| INFO | Dashboard and status page only | **not attached** | Look when convenient |

**Why not separate PAGE/WARN notification objects.** An earlier draft of this design had
`T1-PAGE-telegram` and `T2-WARN-telegram`. Both would have pointed at the same bot and the same
chat, so they would have been two objects doing byte-for-byte identical things — speculative
structure for a routing decision that has not been made. The distinction that actually does work
is attached vs not attached, and that needs no new objects.

Severity lives in the **monitor name** instead: a message about `PROD Manager API` reads as more
urgent than one about `DEV Manager API`, at zero cost.

**Revisit when there is a second destination** — a separate chat for real pages, quiet hours, or
Grafana OnCall. At that point create the second notification and re-attach the handful of urgent
monitors. With roughly a dozen monitors that is a few minutes of clicking, and the decision gets
made against real alert-volume data from the soak rather than guessed at now.

**`Default enabled` must stay unchecked** on the notification. With it on, every monitor created
from then on is silently attached, and nothing can be dashboard-only — which collapses the only
distinction this scheme has.

**The governing rule:** a monitor that would not cause anyone to act is INFO, or does not exist.
A permanently-red monitor trains the reader to ignore every other alert — that is exactly how the
ElevenLabs 401 went unnoticed for twelve days.

## Dev monitors

Probes go through `otadev.cheekoai.in`, not raw IPs, so they exercise Caddy, TLS, cert expiry, and
routing — the layers that fail first. All endpoints verified answering 200 on 2026-08-31.

| Name | Tier | Type | Target | Interval | Retries | Notes |
|---|---|---|---|---:|---:|---|
| DEV Manager API (public) | ALERT | http | `https://otadev.cheekoai.in/toy/health` | 60 | 2 | Through Caddy; gives TLS expiry tracking for free |
| DEV Manager API DB | ALERT | keyword | `https://otadev.cheekoai.in/toy/health/db` | 120 | 2 | Keyword `"database":"connected"`. **Must be keyword, not status code** — this endpoint returns 200 even when the DB is unhappy |
| DEV MQTT Gateway | ALERT | http | `http://64.227.170.31:8004/health` | 60 | 2 | Not proxied by Caddy; direct IP is correct here |
| DEV EMQX MQTT | ALERT | port | `64.227.170.31:1883` | 60 | 2 | Device transport |
| DEV LiveKit | ALERT | port | `64.227.170.31:7880` | 60 | 2 | Currently paused in Kuma; re-enable |
| DEV process health | ALERT | push | script-driven, see plan Task 7 | 300 | 1 | pm2 crash-loop detection. Heartbeat interval doubles as a dead-man's switch |

## Dependency monitors

Shared across environments. Authenticated probes, not TCP `:443` connects — a port check cannot
detect a revoked key, which is the failure that actually happened.

| Name | Tier | Type | Target | Interval | Notes |
|---|---|---|---|---:|---|
| Sarvam TTS (authenticated) | ALERT | keyword POST | `https://api.sarvam.ai/text-to-speech` | 3600 | Real synth probe, keyword `audios`. Minimum sample rate and 2-char text to keep cost negligible |
| LiveKit Cloud | ALERT | port | `cheeko-prod-68ib8ma4.livekit.cloud:443` | 60 | Voice transport |
| CloudFront CDN | ALERT | port | `dsmzc13oafp54.cloudfront.net:443` | 180 | `CLOUDFRONT_DOMAIN` is set on dev |
| ElevenLabs API | INFO | http | `https://otadev.cheekoai.in/toy/health/deps/elevenlabs` | 3600 | **Retired, returns later.** Currently 401 (billing). Dashboard only — no notification attached, so it stays visible without training anyone to mute Telegram |
| Gemini API | INFO | http | `https://otadev.cheekoai.in/toy/health/deps/gemini` | 3600 | Only guards `founderDashboard.service.js`, not the voice path |

## Retired on dev

Removed because the integration is code-present but runtime-off — `manager-api`'s `.env` on
`64.227.170.31` sets none of their keys.

**Verify prod's `.env` independently before removing these there.** Prod may have them enabled, and
retiring a live integration's monitor is precisely the blind spot this project exists to close.

| Name | Reason |
|---|---|
| Qdrant Cloud Port 443 | `QDRANT_*` unset on dev (code lives in `qdrant.service.js`, `rfid.routes.js`) |
| Mem0 API Port 443 | `MEM0_*` unset on dev (code lives in `mem0.service.js`, `agent.service.js`) |
| Grafana Loki Port 443 | `LOKI_*` unset on dev; log shipping unconfirmed. `logger.js` only says the *format* matches Loki |
| Uptime Kuma Self Check | Kuma cannot report its own death. Replaced by an external dead-man's switch (plan Task 11) |
| Gemini API Port 443 | TCP connect proves nothing the authenticated probe does not |
| ElevenLabs API Port 443 | Same |
| Manager API Health (raw IP) | Superseded by the `otadev.cheekoai.in` probe, which also tests Caddy and TLS |
| Manager API DB Health (raw IP) | Same |
| Manager Web Health (raw IP) | Same. Interval had also drifted 60 → 600s unnoticed |
| MQTT Gateway Health (raw IP) | Rebuilt as `DEV MQTT Gateway` |
| EMQX MQTT Port 1883 (raw IP) | Rebuilt as `DEV EMQX MQTT` |
| LiveKit Local Port 7880 (raw IP) | Rebuilt as `DEV LiveKit` |

## EKS

**Not monitored by Kuma.** `picoclaw-livekit` in `picoclaw-dev` is ClusterIP-only, so an external
prober cannot reach it. Covered instead by Prometheus + Alertmanager inside the cluster — see plan
Tasks 8 and 9. Prometheus is already scraping those pods to feed the HPA; it just has no alerting
wired to it yet.

## Change log

- **2026-08-31** — Dropped the T1/T2 notification split before building it: both would have pointed
  at the same bot and chat, so the only real distinction is attached vs not attached.
- **2026-08-31** — Initial version. Replaces the stale table in
  `cheeko-backend/README_UPTIME_KUMA.md`, which described monitors on a retired host.
