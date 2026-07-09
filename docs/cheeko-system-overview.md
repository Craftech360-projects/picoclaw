# Cheeko Platform — Complete System Overview

> Cross-repo reference for the Cheeko kids' voice-AI toy platform. Compiled 2026-07-09 from code
> exploration of all five components. Repo paths are local dev paths on this machine.

## Components at a glance

| Component | Path | Stack | Port(s) |
|---|---|---|---|
| Manager API | `D:\cheeko-backend\main\manager-api-node` | Node/Express 4 + Prisma 7 (Supabase Postgres) | 8002 (`/toy` context path) |
| MQTT Gateway | `D:\cheeko-backend\main\mqtt-gateway` | Node (CommonJS), EMQX, LiveKit SDK | UDP 8884 (audio), 8091 (internal HTTP), 8004 (health) |
| LiveKit Voice Worker | `D:\picoclaw` (`cmd/picoclaw-livekit`) | Go, LiveKit agent worker, in-process picoclaw agent | health port optional |
| Line-Art / AI-Imagine server | `D:\line_art` | Python FastAPI + FLUX image gen | 8090 (WS `/ws`), deps: Speaches 8001, ComfyUI 8188 |
| Device simulator | `D:\cheeko-backend\client.py` | Python (paho-mqtt, pyaudio, opuslib) | — |

Other pieces referenced: EMQX broker (1883), Cerebrium media API (music/story bots), Qdrant (RFID RAG),
Mem0 (memories), AWS S3 + CloudFront (`cdn.cheekoai.in` / `dsmzc13oafp54.cloudfront.net`), Firebase (parent app auth).

## Big picture

```
ESP32 toy (mic/speaker, RFID, LCD/printer)
   │  MQTT (JSON control)          │  UDP (AES-128-CTR encrypted Opus audio)
   ▼                               ▼
EMQX broker ──republish──> mqtt-gateway ────────────── UDP :8884
                              │      │
        HTTP X-Service-Key    │      │ LiveKit room + AgentDispatch
                              ▼      ▼
                       manager-api-node    LiveKit Cloud/server
                       (personas, devices,        │ job dispatch
                        providers, RFID,          ▼
                        S3 uploads)        picoclaw-livekit worker (Go)
                              ▲            STT → picoclaw agent (LLM) → TTS
                              │ HTTP (persona, providers,
                              │  workspace sync, token usage)
                              │
                       line_art server (FastAPI :8090)
                       ◄── WS from gateway (ai_imagine feature only)
```

Two audio planes:
- **Conversation plane**: device ⇄ gateway over encrypted UDP ⇄ LiveKit room ⇄ picoclaw worker.
- **Imagine shortcut**: device ⇄ gateway ⇄ line_art WS (bypasses LiveKit entirely).

---

## 1. Manager API (`manager-api-node`)

The source of truth for devices, characters (agents), providers, RFID content, and parent-app data.

- **Startup**: `server.js` → prisma generate/migrate → `src/app.js` → listens on 8002, all routes under `/toy`. Swagger at `/toy/doc.html`. Admin dashboard (password-gated) at `/admin-dashboard`.
- **Auth, 3 modes**: custom JWT (web admin), Firebase ID tokens (`/api/mobile/*`, parent Flutter app), `X-Service-Key: SERVICE_SECRET_KEY` (gateway, picoclaw worker, line_art).
- **Route groups** (`src/routes/index.js`, logic in `src/services/*`):
  - `/agent` — character CRUD, persona templates, `prompt/:mac`, `config/:mac`, `device/:mac/{bootstrap,workspace-files,workspace-sync,workspace-lock,sessions,memory}`, `set-character`/`cycle-character`/`current-character`, chat history, MCP tools.
  - `/device` — register, bind/unbind, assign-kid, OTA firmware CRUD, token-usage ingestion.
  - `/api/mobile` — parent app (kids, agents, devices, imagine feed, binding).
  - `/admin/rfid` — RFID cards/packs/series/questions, Qdrant RAG lookup, tap logs/analytics, content downloads.
  - `/livekit`, `/models`, `/config`, `/ttsVoice` — provider config; `GET /toy/livekit/providers/active` returns active LLM/STT/TTS/moderation/image providers with API keys. `PUT /livekit/providers/active/{type}` switches them.
  - `/ota` — `POST /toy/ota/activate` device activation (MAC from `Device-Id` header).
  - `/imagine` — `POST /toy/imagine/upload` (service key, multer, JPEG ≤200KB) → S3 `imagine/` prefix → CloudFront URL. Parent app reads via `GET /api/mobile/devices/:mac/imagine`.
  - `/device-sync` — device⇄app settings sync; calls gateway internal API to push `settings_update` to devices.
- **DB highlights** (`prisma/schema.prisma`, ~65 models): `ai_device` (mac, user_id, agent_id, kid_id, mode) ⇄ `ai_agent` (character: system_prompt, soul, runtime_agent_name, language, model FKs) ⇄ `ai_agent_template`; `parent_profile`/`kid_profile`; provider tables `{llm,stt,tts,moderation,image}_providers`; `voice_sessions`/`voice_session_messages`/`voice_session_summaries`; `device_token_usage*`; `device_memories`, `device_workspace_artifacts`, `workspace_locks`; big RFID subsystem (`rfid_card_mapping`, packs, tap logs); `device_settings`/`device_runtime_state`; daily rollups; `openclaw_pair_tokens`.
- **Outbound calls**: mqtt-gateway internal (`http://127.0.0.1:8091/internal/settings/publish-update`, X-Service-Key) at `deviceSettings.service.js:602`; Qdrant, Mem0, S3/CloudFront, Firebase Admin/FCM, SMTP daily reports.
- **Does NOT**: mint LiveKit tokens, connect to MQTT broker directly, or dispatch agents. It is pulled from, not pushing (except the settings-publish call to the gateway).
- ⚠️ `.env` in working tree contains live DB/AWS/SMTP secrets.

## 2. MQTT Gateway (`mqtt-gateway`)

The device↔cloud bridge. Owns the MQTT broker connection, the UDP audio socket, and LiveKit room/dispatch orchestration.

- **Entry**: `app.js` → `gateway/mqtt-gateway.js` (3843-line monolith) + `mqtt/virtual-connection.js` + `livekit/livekit-bridge.js`. Health on 8004, internal command HTTP on 127.0.0.1:8091.
- **MQTT topics**: subscribes `internal/server-ingest` (EMQX rule wraps every device msg as `{sender_client_id, orginal_payload}` — note misspelling) and `devices/+/data`. Publishes to device on `devices/p2p/{clientId}`, to parent app on `app/p2p/{mac}`. Device clientId = `GID_xxx@@@<mac_underscored>@@@<uuid>`.
- **Session start**: device `hello` (version must be 3) → fast path replies with `{session_id, udp:{server,port,key,nonce,connection_id}, audio_params:{24000Hz,1ch,60ms,opus}}` in <50ms → `_deferredSetup`: queries manager-api (mode, character → `runtimeAgentName`, child profile), creates LiveKit room `{uuid}_{mac}_{roomType}`, `AgentDispatchClient.createDispatch` with room metadata (character, language, child_profile, memories via Mem0), 25s agent-join failsafe.
- **UDP audio protocol**: 16-byte header `[type:u8=1][flags:u8][payload_len:u16BE][connection_id:u32BE][timestamp:u32BE][sequence:u32BE]` + AES-128-CTR-encrypted Opus payload; **IV = the header itself**; per-session random key. Replay guard drops old sequences. Device 24kHz Opus in → decrypt → LiveKit; agent 48kHz out → resample 24kHz/60ms frames → encrypt → UDP.
- **Device message types handled**: `hello, listen, abort, goodbye, speech_end, mcp, function_call(play_music/play_story), playback_control, mode-change, settings_*, device_state, analytics_event, card_lookup, download_request, character-change, start_greeting`.
- **RFID flow**: `card_lookup` → manager-api `/admin/rfid/card/lookup/{uid}` + tap logging → replies `card_content` (SD-card content manifest) / `card_ai` (character switch → fresh LiveKit dispatch) / `card_up_to_date` / `card_unknown`.
- **AI Imagine**: `hello.feature == "ai_imagine"` skips LiveKit; buffers raw Opus; on `speech_end` streams to line_art over `LINE_ART_WS_URL`; receives JPEG bytes; POSTs to manager-api `/toy/imagine/upload`; publishes `image{url}` to device over MQTT (90s timeout `IMAGINE_TIMEOUT_MS`).
- **Music/story**: dispatches Cerebrium bots (`/start-music-bot`, `/start-story-bot`) instead of the conversation agent for those modes.
- **Control to agent**: forwarded over LiveKit data channel (`ptt_event`, `speech_end`, `abort`, `end_prompt`, `shutdown_request`, `session_language_update`).
- **Timers**: keepalive 15s, inactivity 2min, max session 60min, ghost-room cleanup every 5min.
- **Stale files**: `gateway/{emqx-broker,udp-server,udp-forwarder,device-handlers,playback-control}.js`, `mqtt/message-parser.js`, root `mqtt-protocol.js`, and the README's "WebSocket bridge" description are dead/superseded. Trust `.env.example`.

## 3. LiveKit Voice Worker (`picoclaw`, this repo)

Go binary `cmd/picoclaw-livekit` — a persona-agnostic LiveKit agent worker running the picoclaw agent **in-process** (not CLI). One worker serves many characters.

- **Worker loop**: `pkg/livekit/worker.go` registers with LiveKit as `--agent-name` (e.g. `cheeko-agent`), accepts dispatched jobs (capacity `MaxSessions`, default 100). Per job: `bridgeFactory` (main.go:332) → `AgentBridge`; `RoomFactory` (main.go:966) → `RoomSession.Join` (publishes `picoclaw-tts` PCM track).
- **Session bootstrap**: parses room metadata (`bootstrap_metadata.go`: child_profile, memories, language, character), pulls persona fresh from manager-api (`manager_workspace_bootstrap.go`: `GET /agent/character/by-name/{name}/session` → systemPrompt + soul + runtimeAgentName), fetches active providers (`manager_provider_runtime.go`: `GET /livekit/providers/active`, 30s TTL cache) which override local config.
- **Workspace**: per-device dir `workspace-<agentID>`, hydrated each session (`workspace_hydration.go`): `AGENT.md` (scaffold + persona in `<!-- PERSONA -->` slot + parent rules from `parent_rules.go`), `SOUL.md`, `USER.md`, `memory/MEMORY.md`, skills. Synced up/down via manager-api workspace endpoints; guarded by distributed lock (`manager_workspace_lock.go`) — a newer session preempts the old one (heartbeat → `RequestTeardown`; preempted session skips upload).
- **Voice pipeline** (`pkg/livekit/audio_pipeline.go`, `room_session.go`): remote track → 16kHz mono → STT stream + TEN VAD (cgo, `pkg/voice/vad`, threshold 0.7, endpoint 1000ms) → transcripts → `AgentBridge.ChatStream` (history + summary + voice directive + tool allowlist) → chunked TTS → local track. Barge-in cancels active TTS on user speech or gateway `abort`. Greeting modes: dynamic (LLM), fallback (static), disabled.
- **Providers**: STT factory (`pkg/voice/stt/factory.go`) — deepgram (default nova-2), groq, assemblyai, openai, cartesia, elevenlabs, azure, google, aws, soniox, sarvam, etc.; fed by manager API or Postgres fallback. TTS builders: elevenlabs, inworld, cartesia, deepgram (default aura-2, pcm_24000).
- **Session end**: participant disconnect / `end_prompt` / `shutdown_request` / preemption → persist usage + transcript to manager-api (`post_session_persistence.go`), upload workspace, close agent, delete workspace dir.
- **Key env**: `livekit_service.*` in config.json (server_url, api_key/secret), `PICOCLAW_LIVEKIT_MANAGER_API_URL` (default `http://localhost:8002/toy`), `PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY`, `PICOCLAW_VAD_THRESHOLD`, `PICOCLAW_VAD_ENDPOINT_MS`.
- **Docs**: `CONTEXT.md` is authoritative; `docs/multi-character-card-flow.md`, `docs/adr/`. `livekit_voice_agent_architecture.md` is partly dated (mentions IDENTITY.md; reality is AGENT.md/SOUL.md + manager-driven providers).

## 4. Line-Art / AI-Imagine Server (`line_art`)

FastAPI service generating images from a child's spoken prompt. Single real endpoint: **`WS /ws`** on port 8090 (plus `GET /health`).

- **Protocol autodetect**: first WS frame `{"type":"hello"}` → device protocol (`app/device_protocol.py`); else browser test protocol. Hello field `feature=="ai_imagine"` selects imagine mode; otherwise printer/line-art mode.
- **Pipeline**: raw Opus frames (16kHz mono 60ms, decoded via opuslib) → STT (Speaches local Whisper / Groq / Deepgram / Sarvam — selection from manager-api `GET /providers/active` with `X-Service-Key`) → subject cleanup + safety (keyword + Groq LLM moderation) → FLUX.1-schnell image gen.
- **Two outputs**: printer mode → 384px-wide 1-bit packed mono bitmap (`raw_mono`, MSB-first, 48 B/row, confirm-gated via `print_confirm`); imagine mode → 320×240 baseline JPEG ≤200KB sent as `image` message (base64/bytes).
- **Image backends** (`app/image_gen.py`): `IMAGE_BACKEND=comfyui` → local ComfyUI :8188 (FLUX fp8); else cloud chain hf/runware/fal, active provider from manager-api, 75s chain deadline, fallback image on failure.
- **Storage**: does not touch S3 itself (ADR-0001) — the gateway uploads the returned JPEG to manager-api → S3/CloudFront, then sends the URL to the device.
- **Run**: `uvicorn app.main:app --port 8090`; pm2 `lineart` at `/opt/line_art` in prod; optional `WS_SHARED_SECRET` auth on hello.

## 5. Device Simulator (`client.py`)

Python CLI mimicking the ESP32 toy. Modes: `--mode voice` (default), `rfid`, `imagine`.

- **Voice flow** (mirrors firmware): ① `POST http://{SERVER_IP}:8002 or 8002-proxied OTA}/toy/ota/` with `device-id` header → gets MQTT credentials + websocket URL (+ optional activation code loop) → ② MQTT connect (clientId `GID_test@@@mac@@@uuid`, HMAC-SHA256 password signed with `MQTT_SIGNATURE_KEY`), subscribe `devices/p2p/{clientId}` → ③ publish `hello` on `device-server` topic, receive UDP session (key/nonce/session_id), send encrypted UDP `ping` → ④ publish `listen{state:detect}` → server TTS streams down (jitter-buffered playback, sequence-gap tracking); `tts stop` triggers mic capture; Opus-encoded mic frames stream up over encrypted UDP. Spacebar = `abort` (barge-in); number keys = mid-session RFID tap (`card_lookup` → character switch); `goodbye` on exit.
- **RFID mode**: `card_lookup` with version/hash handshake → expects `card_up_to_date|card_content|card_ai|card_unknown`; optional `download_request` and tap-analytics fetch from manager-api.
- **Imagine mode**: `hello` with `feature:"ai_imagine"`, hold-SPACE push-to-talk, `speech_end` triggers generation, waits for `image{url}` and downloads it.
- Encryption/framing code in `encrypt_packet` matches the gateway's UDP protocol exactly (header-as-IV AES-CTR).

---

## End-to-end flows (cheat sheet)

**Voice conversation**: toy OTA-activates + binds (parent app) → hello over MQTT → gateway creates LiveKit room + dispatches `cheeko-agent` with metadata → picoclaw worker pulls persona + providers from manager-api, hydrates workspace → child speaks (UDP→LiveKit→STT→VAD→LLM→TTS→UDP) → session ends → transcript/usage/workspace persisted to manager-api.

**Character switch (RFID)**: tap card → `card_lookup` → gateway asks manager-api → `card_ai` → manager `set-character` → new dispatch with new metadata; workspace lock preempts the old session; USER.md/memory persist across characters, AGENT.md/SOUL.md swap.

**AI Imagine**: hello `feature:ai_imagine` → gateway buffers Opus → `speech_end` → line_art WS (STT+moderation+FLUX) → JPEG → gateway uploads to manager-api → S3/CDN → `image{url}` to device → LCD fetches and displays; also appears in the parent app feed.

**Settings sync**: parent app → manager-api `/device-sync` → gateway internal `POST :8091/internal/settings/publish-update` → MQTT `settings_update` → device acks.

## Shared secrets / auth map

- `SERVICE_SECRET_KEY` — one shared service key: gateway→manager-api, picoclaw→manager-api, line_art→manager-api, manager-api→gateway internal.
- `MQTT_SIGNATURE_KEY` — HMAC for device MQTT passwords (gateway + OTA credential issuance + client.py).
- LiveKit `API_KEY/API_SECRET` — gateway (tokens + dispatch) and picoclaw worker.
- Firebase — parent app only. JWT — web admin only.
