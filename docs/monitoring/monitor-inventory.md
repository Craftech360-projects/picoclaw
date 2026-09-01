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

### Provider monitors follow the database, they do not name a vendor

`LLM provider (active)`, `STT provider (active)` and `TTS provider (active)` are **push** monitors
driven by `provider-probe.sh`, which reads the active provider rows at runtime and dispatches to a
per-vendor probe. Switching providers in the admin UI needs **no change in Kuma** — the probe
follows. Messages name what was found, e.g. `tts ok (smallest/lightning_v3.1/liam, 38400B)`.

This replaced `Sarvam LLM/STT (authenticated)` and `SmallestAI TTS (authenticated)`, each of which
named a vendor and would have gone stale on the next switch — the failure that actually occurred
within a day of building them.

**An unknown vendor pushes DOWN, it does not pass silently:**

```
no probe implemented for TTS provider 'acme-newvendor' - add one to provider-probe.sh
```

Verified 2026-09-01 by forcing that branch. This is the part that must not be automated away: a
vendor nobody has written a probe for is exactly when a human should be told.

Probes implemented: LLM `sarvam*` (api-subscription-key) and `openrouter*`/`*gpt*`/`mistral*`
(Bearer); STT `sarvam*`; TTS `smallest`, `sarvam`, `elevenlabs`. Adding a vendor is a few lines in
the case statement. If the list grows past a handful, move this into the Go agent as a self-test
endpoint rather than extending the script.

**The drift monitor is now INFO** (no notification attached). Since the probes follow changes on
their own, a provider switch no longer breaks monitoring, so drift is a record that configuration
moved rather than a call to action. The baseline is still **never** auto-updated — automating that
would mean never being told a provider changed at all.

### 2026-09-01: the drift monitor earned its place

A dev deploy switched TTS from Sarvam to SmallestAI. The drift monitor caught it within the hour:

```
11:17  providers unchanged
12:17  provider config changed - update Kuma monitors ...
```

Without it, `Sarvam TTS (authenticated)` would have gone on reporting green against a provider the
application no longer calls — the exact silent-drift failure the monitor exists to prevent. It has
now paid for itself once, one day after being built.

**Actions taken:** added `SmallestAI TTS (authenticated)`; deleted `Sarvam TTS (authenticated)`
(Sarvam's key is still exercised by the LLM and STT monitors, so no credential coverage was lost);
refreshed `/var/lib/kuma-provider-baseline.txt`.

**Bug found and fixed in the same pass.** The alert that arrived read *"No heartbeat in the time
window"*, not the real message. Cause: the push monitors had `retry_interval=300` while only
reporting every 900–3900s. On a `down` push Kuma switched to its 300-second retry window, found no
heartbeat because the script had not run again yet, and overwrote the useful message with a generic
one. Fixed by setting `retry_interval = interval` on every push monitor.

**Rule: a push monitor's `retry_interval` must be at least its reporting interval**, or the real
failure message gets replaced by a false "no heartbeat" one — which reads like the monitoring broke
rather than like it worked.

### The voice path spans Sarvam and SmallestAI

Verified against the provider tables on 2026-08-31. **3 active providers out of 31 rows:**

| Stage | Row | Model | Endpoint |
|---|---|---|---|
| LLM | `llm_providers` id 10, `sarvam-gemma4` | stored as `openai/gemma4`, sent as `gemma4` | `https://api.sarvam.ai/v2/chat/completions` |
| STT | `stt_providers` id 2960, `sarvam_rest` | `saaras:v4` | `https://api.sarvam.ai/speech-to-text` |
| TTS | `tts_providers` id 14, `smallest` | `lightning_v3.1` / `liam` | `https://waves-api.smallest.ai/api/v1/lightning-v3.1/get_speech` |

LLM and STT are Sarvam and share one key; TTS moved to SmallestAI on 2026-09-01 with its own key. OpenRouter (`google/gemma-4-31b-it`) was switched off on
2026-08-31 08:00 and replaced by `sarvam-gemma4` in the same minute. Gemini, GPT-5-mini, Mistral,
Deepgram, ElevenLabs, Cartesia, Azure, xAI: all `is_active=f`.

**Concentration risk, reduced but not gone.** Until 2026-09-01 a single vendor and a single
credential sat behind every stage of a child's conversation. Moving TTS to SmallestAI splits that:
a Sarvam outage or key revocation now costs LLM and STT, not the whole pipeline. Both remaining
stages still share one Sarvam key, so that key is still a single point of failure for understanding
and answering a child — only speech output survives it. The monitors surface this quickly; they
cannot reduce it.

### There are two Sarvam keys — use the database one

| Source | md5 (first 12) | Used by |
|---|---|---|
| `llm_providers` / `stt_providers` / `tts_providers` (active rows) | `9e487d074aeb` | **the running agent** |
| `/root/picoclaw/.env` `SARVAM_API_KEY` on the dev box | `0849ee410b45` | nothing that matters |

The first version of monitor 25 was built from the `.env` key. It went green, and it was worthless:
it exercised a credential the application never presents. Had the real key been revoked, the monitor
would have stayed green through the outage.

The two keys are not interchangeable — the `.env` one is rejected from `/v2/chat/completions` with
*"This endpoint is currently in beta and not available"*, while the database one succeeds. That
difference is what exposed the mistake.

**Rule: probe the credential the application reads, not the one that is easiest to find.** When
provider config lives in a database, `.env` is the wrong source.

| Name | Tier | Type | Target | Interval | Notes |
|---|---|---|---|---:|---|
| SmallestAI TTS (authenticated) | ALERT | http POST | `https://waves-api.smallest.ai/api/v1/lightning-v3.1/get_speech` | 3600 | `Bearer` auth, voice `liam`, model `lightning_v3.1`. Response is raw PCM (`application/octet-stream`), so status-code only — a keyword match against binary audio would be meaningless |
| Sarvam LLM (authenticated) | ALERT | keyword POST | `https://api.sarvam.ai/v2/chat/completions` | 3600 | Real completion, `model: gemma4`, `max_tokens: 5`, keyword `choices` |
| Sarvam STT | ALERT | **push** | `https://api.sarvam.ai/speech-to-text` | 3600 | Kuma's HTTP monitor cannot send multipart, so the dev-box script does the upload and pushes the verdict. Probe is a 0.25s silence WAV generated by Python's `wave` module — no ffmpeg needed. Success is `request_id` in the response; an empty `transcript` is expected and fine |

### Config drift

The endpoint monitors above hardcode Sarvam URLs and the Sarvam key. Provider selection lives in
the database and can be changed from an admin UI without anyone touching Kuma — at which point the
monitors keep probing a provider that is no longer in use and report green through a real outage.
The active LLM was in fact switched once already on 2026-08-31.

Relying on "remember to update Kuma" is the same discipline that already failed here, so drift is
monitored rather than trusted:

| Name | Tier | Type | Interval | Notes |
|---|---|---|---:|---|
| DEV provider config drift | ALERT | push | 3600 | Hashes the active rows of `llm_providers`, `stt_providers`, `tts_providers` and compares against a recorded baseline. Pushes down naming what changed. Does not prevent drift — makes it loud instead of silent. Update the baseline deliberately after an intended provider change |
| LiveKit Cloud | ALERT | port | `cheeko-prod-68ib8ma4.livekit.cloud:443` | 60 | Voice transport |
| CloudFront CDN | ALERT | port | `dsmzc13oafp54.cloudfront.net:443` | 180 | `CLOUDFRONT_DOMAIN` is set on dev |
| ElevenLabs TTS (switch-back readiness) | INFO | keyword POST (inverted) | `https://api.elevenlabs.io/v1/text-to-speech/hO2yZ8lxM3axUxL8OeKX` | 21600 | **Retired, returns later.** Direct probe on the `tts_providers` key, `eleven_multilingual_v2`. Inverted keyword `payment_issue`. Dashboard only — no notification. Red today; **turning green is the signal that ElevenLabs can be switched back on** |

**ElevenLabs status, tested 2026-08-31.** The key is valid — the blocker is billing, not credentials:

```
POST /v1/text-to-speech/... -> 401 payment_issue
"Your subscription has a failed or incomplete payment. Complete the latest invoice to continue usage."
```

`/v1/user/subscription` also returns 401, but for an unrelated reason (`missing_permission: user_read`) —
the key is TTS-scoped, which is normal. So switching back needs the **invoice settled**, not a new key;
the key, voice id and model in `tts_providers` row 1 all remain usable.

`capacity-and-hardening.md` records the same `payment_issue` on 2026-06-12, so this has been
outstanding since June.

This monitor deliberately replaced the old id-18 monitor, which went through `manager-api` on the
**retired prod box** and tested manager-api's env key rather than the database key a switch-back
would actually use — the same wrong-credential trap as the Sarvam monitor.
| Gemini API | INFO | http | `https://otadev.cheekoai.in/toy/health/deps/gemini` | 3600 | Only guards `founderDashboard.service.js`, not the voice path |

## Prod monitors (`ota.cheekoai.in`, box `139.59.7.72`)

Prod is **not** a copy of dev — verified 2026-08-31, and the differences matter:

| Name | Tier | Type | Target | Interval |
|---|---|---|---|---:|
| PROD Manager API (public) | ALERT | http | `https://ota.cheekoai.in/toy/health` | 60 |
| PROD Manager API DB | ALERT | keyword | `https://ota.cheekoai.in/toy/health/db` — `"database":"connected"` | 120 |
| PROD MQTT Gateway | ALERT | http | `http://139.59.7.72:8004/health` | 60 |
| PROD EMQX MQTT | ALERT | port | `139.59.7.72:1883` | 60 |
| PROD process health | ALERT | push | pm2: `manager-api manager-web gw-0..3` | 900 |
| Mem0 API (authenticated) | ALERT | http | `https://api.mem0.ai/v1/memories/?user_id=healthcheck` + `Authorization: Token` | 300 |
| Qdrant Cloud (reachability) | INFO | http | `.../collections` | 600 |

Differences from dev:

- **`MEM0_API_KEY`, `QDRANT_API_KEY`, `QDRANT_URL` are set on prod** and unset on dev. Retiring
  those monitors on dev was right; retiring them on prod would have removed coverage of live
  integrations. This is exactly why the plan required checking prod's env independently.
- **No `picoclaw-livekit` on this box** — the voice agent runs on EKS and is covered by Prometheus
  and Alertmanager, not Kuma. The pm2 watch list is adjusted accordingly.
- `LOKI_*` unset here too, so Loki really is unused everywhere.

### Qdrant is broken, and the old monitor said it was fine

The retired `Qdrant Cloud Port 443` monitor was **green** the moment it was deleted. Measured from
the prod box on 2026-08-31:

| Check | Result |
|---|---|
| DNS | resolves, 3 addresses |
| **TCP connect to 443** | **OPEN** — the entirety of what the old monitor tested |
| TLS request | `Recv failure: Connection reset by peer` |

The load balancer accepts the connection; the cluster behind it resets. Consistent with a suspended
Qdrant Cloud cluster. Kuma now reports `read ECONNRESET`.

This is the clearest evidence in the whole project for why TCP port checks are not monitoring: the
service is unusable and the check was green. The RFID *admin* endpoints still work because they are
Postgres-backed; no user-facing breakage has been demonstrated, only that Qdrant is unreachable and
nothing said so.

Left **INFO** deliberately — attaching a notification to an already-broken monitor is how the
ElevenLabs alert got muted. Promote it to ALERT once Qdrant is fixed, or delete it if Qdrant is
being dropped.

## Status page

`http://16.112.52.71:3001/status/cheeko` — slug `cheeko`, all 20 monitors in four groups:
Production, Voice providers, Development, Informational.

`published=0` and `search_engine_index=0`. The whole instance already sits behind the admin CIDR,
so the page is team-facing by construction. Making it genuinely public is a network change and
would need TLS in front first — not a checkbox.

Two stale published pages predated this work and were **deleted 2026-08-31**: `localsdev` (0
monitors — everything on it was removed in Task 4) and `production` (3 monitors, a misleading
subset that implied prod was three services). Both were `published=1` and would have shown a
false picture to anyone who had the link. `cheeko` is now the only status page.

## Watching the watcher

`kuma-deadman.sh` on the dev box curls Kuma every 5 minutes and messages Telegram **directly**
(bypassing Kuma entirely) after two consecutive failures, with a recovery message when it returns.

Kuma cannot report its own death — if the instance stops or the disk fills, every monitor goes quiet,
and quiet is indistinguishable from healthy. The dev box already had cron and the bot token, so this
needed no new service and no new account. Dev watches Kuma; Kuma watches dev via monitors 20-24 and
27. The accepted ceiling is that both hosts failing simultaneously is uncovered; a third-party check
such as healthchecks.io would close that if it ever matters.

`/etc/cron.d/kuma-monitoring` is mode `600` because it carries the bot token.

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
