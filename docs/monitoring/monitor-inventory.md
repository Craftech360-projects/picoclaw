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

| Tier | Meaning | Notification object | Action expected |
|---|---|---|---|
| T1 PAGE | A child cannot talk to their toy right now | `T1-PAGE-telegram` | Drop everything |
| T2 WARN | Degraded but still serving | `T2-WARN-telegram` | Look today |
| T3 INFO | Dashboard and status page only, nothing attached | none | Look when convenient |

Both tiers point at the same Telegram chat for now. The split exists so severity can be routed
differently later (Grafana OnCall, PagerDuty) without re-tagging every monitor.

**The governing rule:** a monitor that would not cause anyone to act belongs in T3, or does not
exist. A permanently-red monitor trains the reader to ignore every other alert — that is exactly
how the ElevenLabs 401 went unnoticed for twelve days.

## Dev monitors

Probes go through `otadev.cheekoai.in`, not raw IPs, so they exercise Caddy, TLS, cert expiry, and
routing — the layers that fail first. All endpoints verified answering 200 on 2026-08-31.

| Name | Tier | Type | Target | Interval | Retries | Notes |
|---|---|---|---|---:|---:|---|
| DEV Manager API (public) | T2 | http | `https://otadev.cheekoai.in/toy/health` | 60 | 2 | Through Caddy; gives TLS expiry tracking for free |
| DEV Manager API DB | T2 | keyword | `https://otadev.cheekoai.in/toy/health/db` | 120 | 2 | Keyword `"database":"connected"`. **Must be keyword, not status code** — this endpoint returns 200 even when the DB is unhappy |
| DEV MQTT Gateway | T2 | http | `http://64.227.170.31:8004/health` | 60 | 2 | Not proxied by Caddy; direct IP is correct here |
| DEV EMQX MQTT | T2 | port | `64.227.170.31:1883` | 60 | 2 | Device transport |
| DEV LiveKit | T2 | port | `64.227.170.31:7880` | 60 | 2 | Currently paused in Kuma; re-enable |
| DEV process health | T2 | push | script-driven, see plan Task 7 | 300 | 1 | pm2 crash-loop detection. Heartbeat interval doubles as a dead-man's switch |

## Dependency monitors

Shared across environments. Authenticated probes, not TCP `:443` connects — a port check cannot
detect a revoked key, which is the failure that actually happened.

| Name | Tier | Type | Target | Interval | Notes |
|---|---|---|---|---:|---|
| Sarvam TTS (authenticated) | T1 | keyword POST | `https://api.sarvam.ai/text-to-speech` | 3600 | Real synth probe, keyword `audios`. Minimum sample rate and 2-char text to keep cost negligible |
| LiveKit Cloud | T1 | port | `cheeko-prod-68ib8ma4.livekit.cloud:443` | 60 | Voice transport |
| CloudFront CDN | T2 | port | `dsmzc13oafp54.cloudfront.net:443` | 180 | `CLOUDFRONT_DOMAIN` is set on dev |
| ElevenLabs API | T3 | http | `https://otadev.cheekoai.in/toy/health/deps/elevenlabs` | 3600 | **Retired, returns later.** Currently 401 (billing). Dashboard only — no notification attached, so it stays visible without training anyone to mute Telegram |
| Gemini API | T3 | http | `https://otadev.cheekoai.in/toy/health/deps/gemini` | 3600 | Only guards `founderDashboard.service.js`, not the voice path |

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

- **2026-08-31** — Initial version. Replaces the stale table in
  `cheeko-backend/README_UPTIME_KUMA.md`, which described monitors on a retired host.
