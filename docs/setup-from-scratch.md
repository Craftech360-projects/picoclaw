# Setting Up PicoClaw + Cheeko Backend From Scratch

A complete, first-time setup: clone both repos, stand up the database, run the
manager API, the MQTT gateway and the LiveKit voice worker, then load the
characters so a device can hold a conversation.

**This document is written to be handed to Claude Code.** Steps are ordered,
each ends in a checkpoint you can verify, and anything requiring a human
decision or a secret is marked **STOP — ASK THE HUMAN**. Do not invent values
for those; a wrong API key fails loudly, but a wrong database URL can quietly
write to someone else's environment.

For deploying to an environment that already exists, use
[deploy/dev/](../deploy/dev/), [deploy/prod/](../deploy/prod/) or
[deploy/README.md](../deploy/README.md) instead. This document is only for
building a new one.

---

## 1. What you are building

Four processes and one database:

```
  device / toy
       │  MQTT
       ▼
  mqtt-gateway ──────► LiveKit Cloud ◄────── picoclaw-livekit (voice worker)
       │                                            │
       └──────────────► manager-api ◄───────────────┘
                             │
                         Postgres
```

| component | repo | language | what it does |
|---|---|---|---|
| `manager-api` | cheeko-backend | Node | owns the database, characters, banks, progress |
| `mqtt-gateway` | cheeko-backend | Node | device connections, creates LiveKit rooms |
| `picoclaw-livekit` | picoclaw | Go | joins rooms, runs STT → LLM → TTS |
| dashboards | cheeko-backend | Node | admin + parent web UIs (optional for a first run) |

The worker is **stateless about characters**. Personas, prompts and question
banks all come from the manager API at session start, which is why the database
has to be alive and seeded before the worker is useful.

---

## 2. Prerequisites

### Tooling

| tool | version | notes |
|---|---|---|
| Go | **1.25.8+** | `go.mod` pins `go 1.25.8` |
| C toolchain | any | **required** — opus needs cgo, so `CGO_ENABLED=0` will not build |
| Node.js | 20+ | manager-api and gateway |
| PostgreSQL | 14+ | managed (Supabase / DigitalOcean) or local |
| make, git | any | |

On Linux you will also need the C++ runtime for the VAD library:

```bash
sudo apt-get install -y build-essential pkg-config libopus-dev
```

### Accounts and credentials

**STOP — ASK THE HUMAN for every value below.** None can be guessed.

| service | needed for | where it goes |
|---|---|---|
| **LiveKit Cloud** (URL, API key, API secret) | the voice room | `.security.yml` + gateway `.env` |
| **PostgreSQL** connection string | everything | manager-api `.env` |
| **LLM provider** (OpenRouter, xAI, …) | the agent's replies | `.security.yml` |
| **Sarvam** API key | Indian-language STT/TTS | worker `.env` |
| **ElevenLabs** key | alternative TTS | `.security.yml` |
| **Deepgram** key | alternative STT | `.security.yml` |
| **EMQX** host/port + signature key | MQTT broker | gateway `.env` |

A minimal first run needs: LiveKit, Postgres, one LLM provider, one STT, one
TTS. Everything else can stay empty.

---

## 3. Clone

```bash
git clone https://github.com/Craftech360-projects/picoclaw.git
git clone https://github.com/Craftech360-projects/cheeko-backend.git
```

Both default to `main`. Confirm before going further:

```bash
git -C picoclaw log --oneline -1
git -C cheeko-backend log --oneline -1
```

> **Note the repo name mismatch on servers.** Deployed boxes check
> cheeko-backend out as `/root/xiaozhi-esp32-server`. Same repo, historical
> name. Existing docs and scripts use that path.

---

## 4. Database

### 4.1 Create the database

Create an empty Postgres database and get two URLs:

- `DATABASE_URL` — pooled connection, used by the app
- `DIRECT_URL` — direct connection, used by Prisma migrations

On Supabase these differ by port (6543 pooled / 5432 direct). On DigitalOcean
managed Postgres they are usually the same host on port 25060.

### 4.2 Configure and migrate

```bash
cd cheeko-backend/main/manager-api-node
cp .env.example .env
```

Fill in `.env`. The keys that actually matter for a first run:

```
PORT=8002
NODE_ENV=development
DATABASE_URL=postgresql://...        # STOP — ASK THE HUMAN
DIRECT_URL=postgresql://...          # STOP — ASK THE HUMAN
SERVICE_SECRET_KEY=<36-char secret>  # shared with the worker and gateway
DEFAULT_PARENT_TIMEZONE=Asia/Kolkata
LOG_LEVEL=info
CORS_ORIGINS=http://localhost:3000
```

`SUPABASE_*`, `QDRANT_*` and `MEM0_API_KEY` may be left blank unless those
features are being used.

Then:

```bash
npm install
npx prisma generate
npx prisma migrate deploy
```

**Checkpoint:**

```bash
npx prisma migrate status     # "Database schema is up to date"
```

> **`prisma generate` must run before the server starts**, and again after any
> schema change. Skip it and a `select: { new_field }` throws at runtime rather
> than at build time.

> **Migrations may apply on boot.** `server.js` calls `runPrismaMigrations()`
> at startup, so starting the server applies every unapplied migration in the
> tree at once — unless `SKIP_DB_SYNC=1` is set, in which case it applies
> nothing and waits for an explicit `npx prisma migrate deploy`. Decide which
> behaviour you want before the first start. Production sets `SKIP_DB_SYNC=1`.

### 4.3 Start the manager API

```bash
npm start           # or: pm2 start src/server.js --name manager-api
```

**Checkpoint:** the banner prints `Database: Schema Ready` and the port, and

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8002/toy/doc.html
```

returns `200` or `301`.

---

## 5. MQTT gateway

```bash
cd cheeko-backend/main/mqtt-gateway
cp .env.example .env
npm install
```

Fill in `.env`:

```
UDP_PORT=8884
PUBLIC_IP=<this host's reachable IP>     # STOP — ASK THE HUMAN
EMQX_HOST=<broker host>                  # STOP — ASK THE HUMAN
EMQX_PORT=1883
EMQX_PROTOCOL=mqtt
MQTT_SIGNATURE_KEY=<shared with firmware>  # STOP — ASK THE HUMAN
MANAGER_API_URL=http://localhost:8002/toy
MANAGER_API_SECRET=<same SERVICE_SECRET_KEY as above>
```

`AWS_*` and recording flags are optional; leave `AUDIO_RECORDING_ENABLED=false`
for a first run.

### 5.1 `config/mqtt.json` — required, and NOT in the repo

The gateway also reads `config/mqtt.json`, loaded at
[`app.js:26`](../../cheeko-backend/main/mqtt-gateway/app.js) via
`new ConfigManager('mqtt.json')`. **This file is gitignored and no template
ships with the repo**, so a fresh clone has nothing to load and the gateway will
not start until you create it:

```bash
mkdir -p config
cat > config/mqtt.json <<'JSON'
{
  "debug": false,
  "mqtt_broker": {
    "host": "<broker host or IP>",
    "port": 1883,
    "protocol": "mqtt",
    "keepalive": 60,
    "clean": true,
    "reconnectPeriod": 1000,
    "connectTimeout": 30000
  },
  "livekit": {
    "url": "wss://<your-project>.livekit.cloud",
    "api_key": "<LiveKit API key>",
    "api_secret": "<LiveKit API secret>"
  }
}
JSON
```

**STOP — ASK THE HUMAN** for the broker host and the LiveKit credentials.

Two things to get right:

- **The LiveKit credentials appear twice** — here and in the worker's
  `~/.picoclaw/.security.yml`. They must be the same project, or the gateway
  creates rooms the worker never joins and sessions hang with no error.
- **`livekit.url` here is the gateway's own connection**, and `protocol`/`port`
  must match the broker's listener (`mqtt`/1883 plain, `mqtts`/8883 TLS).

The local-development defaults in an existing checkout — `ws://localhost:7880`
with `api_key: devkey` — are LiveKit's stock dev-server values. They only work
against a locally run LiveKit server, never LiveKit Cloud.

```bash
node --check server.js && npm start
```

**Checkpoint:** the gateway logs a successful EMQX connection. Without a real
broker and a real device, this component cannot be fully verified — that is
expected, and the worker can still be tested through the admin dashboard.

---

## 6. PicoClaw voice worker

### 6.1 Config file

The worker reads `~/.picoclaw/config.json` for behaviour and
`~/.picoclaw/.security.yml` for secrets. They are separate on purpose — only the
second holds credentials.

```bash
mkdir -p ~/.picoclaw
cp picoclaw/deploy/config.json.example ~/.picoclaw/config.json
```

Expand it to at least this shape:

```json
{
  "version": 1,
  "agents": {
    "defaults": {
      "workspace": "/root/.picoclaw/workspace",
      "provider": "openrouter",
      "model_name": "openrouter",
      "max_tokens": 8192,
      "temperature": 0.7,
      "restrict_to_workspace": true
    }
  },
  "livekit_service": {
    "server_url": "wss://<your-project>.livekit.cloud",
    "health_port": 8193,
    "max_sessions": 12,
    "runtime": {
      "greeting_mode": "dynamic",
      "vad_threshold": 0.5,
      "vad_endpoint_ms": 2000,
      "detailed_trace_enabled": false
    },
    "tts": {
      "provider": "elevenlabs",
      "model_id": "eleven_multilingual_v2",
      "voice_id": "<a voice id>",
      "sample_rate_hz": 24000,
      "output_format": "pcm_24000"
    },
    "stt": {
      "database_url": "postgresql://..."
    }
  }
}
```

> **`detailed_trace_enabled` writes children's conversation content to logs in
> plaintext.** Useful while building, unacceptable with real users. Default it
> to `false` and turn it on deliberately.

> **STT provider selection lives in a database table**, not this file — see
> `docs/stt_postgres_setup.md`. `stt.database_url` points at whichever database
> holds the `stt_providers` table.

### 6.2 Secrets file

`~/.picoclaw/.security.yml` — **never commit this**:

```yaml
providers:
  openrouter:
    api_key: "<STOP — ASK THE HUMAN>"
livekit_service:
  api_key: "<LiveKit API key>"
  api_secret: "<LiveKit API secret>"
  deepgram_api_key: ""
  inworld_api_key: ""
  cartesia_api_key: ""
```

Add `elevenlabs_api_key` under `voice:` if using ElevenLabs TTS.

### 6.3 Worker environment

`picoclaw/.env`:

```
MANAGER_API_URL=http://localhost:8002/toy
MANAGER_API_SECRET=<same SERVICE_SECRET_KEY>
PICOCLAW_LIVEKIT_MANAGER_API_URL=http://localhost:8002/toy
PICOCLAW_LIVEKIT_MANAGER_API_SERVICE_KEY=<same SERVICE_SECRET_KEY>
PICOCLAW_LIVEKIT_MANAGER_SESSION_STORE_ENABLED=true
SARVAM_API_KEY=<STOP — ASK THE HUMAN>
```

> **The `SERVICE_SECRET_KEY` must be byte-identical in all three components.**
> It is sent as an HTTP header, so stray quotes or whitespace make it an invalid
> header and Node rejects the request with a bodiless `400` that looks nothing
> like an auth failure. Verify with
> `grep '^SERVICE_SECRET_KEY=' .env | cut -d= -f2- | tr -d "\"'" | tr -d '[:space:]' | wc -c`
> — expect 37 (36 + newline).

### 6.4 Build and run

```bash
cd picoclaw
export CGO_LDFLAGS='-lc++ -lc++abi'     # Linux; omit on macOS
make build-livekit
./build/picoclaw-livekit
```

**Checkpoint:** the log shows, in order:

```
Health check server started        health=http://0.0.0.0:8192/health
Connected to LiveKit agent endpoint  ws_url=wss://...
Registering worker                 agent=cheeko-agent
Worker registered                  agent=cheeko-agent worker_id=AW_...
```

If it registers, LiveKit credentials and connectivity are correct.

> Cross-compiling from Windows does not work — opus needs cgo. Build on the
> target platform, or in Docker.

---

## 7. Characters, prompts and banks

The database is now schema-complete but **empty of characters**. Nothing will
speak until this step runs.

Prompts and banks live in a separate content project (the "character pack") —
a directory of per-character folders holding `agent.md`, `soul.md`,
`greeting.md` and bank CSVs. **STOP — ASK THE HUMAN** for its location.

```bash
cd cheeko-backend/main/manager-api-node

# dry run first, always
node scripts/install-character-pack.js <pack-dir>
node scripts/install-character-pack.js <pack-dir> --apply

# characters the pack has but the database does not
node scripts/create-missing-character-rows.js <pack-dir> --apply
```

Two behaviours that surprise people:

- **The installer refuses unknown databases.** `EXPECTED_DB` is a hostname
  allowlist; extend it deliberately for a new environment. This is a feature —
  it is what stops a pack landing in the wrong database.
- **The installer updates existing rows and skips absent ones**, printing
  `MISSING row for agent_code=…`. It never sets `agent_name` on an existing row
  either. `create-missing-character-rows.js` covers the first gap; a rename
  needs its own script.

New character rows copy model and voice wiring from Cheeko, so **their voice is
a placeholder** until set explicitly:

```sql
UPDATE ai_agent_template SET sarvam_voice_id = '<voice>' WHERE agent_code = '<code>';
```

**Checkpoint:**

```sql
SELECT agent_code, agent_name, sarvam_voice_id FROM ai_agent_template ORDER BY agent_code;
SELECT count(*) FROM quiz_question;
```

Expect one row per character, distinct voices, and non-zero bank counts.

### The ordering rule

> **Worker code before prompts. Never the reverse.**

Each character ends its replies with a hidden `MEMO: type=<label> | …` line.
That label is three things at once: the scoreboard's filename, its
`kid_character_state.state_type`, and the key the worker's scorer matches on.
A prompt emitting a label the running worker does not recognise records **no
verdicts at all** — the child plays a full game and nothing is saved, silently.

So when a prompt change depends on a worker change, deploy the worker first and
let it soak. The scorer accepts a set of labels rather than one, specifically so
this ordering is always safe.

---

## 7.5 Dashboards (optional, but the easiest way to test)

Neither is required for a device to work, but **`admin-dashboard` is the
fastest way to hold a session without a physical toy** — it joins a LiveKit room
as a browser client, which is how most of the testing in this project is done.

### admin-dashboard

```bash
cd cheeko-backend/main/admin-dashboard
npm install
cat > .env <<'ENV'
PORT=4000
MANAGER_URL=http://localhost:8002/toy
MQTT_SIGNATURE_KEY=<same as the gateway>
LIVEKIT_URL=wss://<your-project>.livekit.cloud
LIVEKIT_PUBLIC_URL=wss://<your-project>.livekit.cloud
LIVEKIT_API_KEY=<LiveKit API key>
LIVEKIT_API_SECRET=<LiveKit API secret>
ADMIN_PASSWORD=<choose one>
ENV
npm start
```

**Checkpoint:** open `http://localhost:4000`, log in with `ADMIN_PASSWORD`, and
start a session. In the worker log you should see `Job assignment received`
followed by `Audio track subscribed` with a `participant=dashboard-…` name.

That participant prefix is worth remembering: `dashboard-…` in the logs means a
browser test client, while a MAC address means a real device. They fail
differently, and confusing the two wastes time when reading traces.

### manager-web

The parent/admin Vue app. It is a pure frontend — it needs the manager API
running, nothing else.

```bash
cd cheeko-backend/main/manager-web
npm install
```

`.env.development` needs:

```
VUE_APP_API_BASE_URL=http://localhost:8002/toy
VUE_APP_TITLE=Cheeko
```

```bash
npm run serve       # dev server
npm run build       # production bundle into dist/
```

**Checkpoint:** the app loads and its network calls to
`VUE_APP_API_BASE_URL` return 200 rather than CORS errors. If they are blocked,
add the dev server's origin to `CORS_ORIGINS` in the manager API `.env` and
restart it.

## 8. End-to-end verification

With all four processes up and a device (or the admin dashboard) connected:

```bash
cd cheeko-backend/main/manager-api-node
node scripts/character-check.js <MAC>
```

A healthy first session shows:

- **CURRENT STATE** — one row per character played, each with its own
  `state_type`. Two characters sharing one row means their prompts share a MEMO
  label.
- **RECENT SESSIONS** — a `parent_summary` once a session completes.
- **SCORED BANKS** — a non-zero count for the bank just played. A state row
  present but `today 0/10` means the MEMO persisted and the verdict was
  rejected: the worker does not recognise that label.

Session traces land in `<workspace>/trace/`. A trace with
`"MessageCount": 1` and a 15–40 second duration is a session that ended right
after the greeting — usually a client disconnect, not an agent fault.

---

## 9. Troubleshooting

### `self-signed certificate in certificate chain`

Newer `pg` treats `sslmode=require` as an alias for `verify-full`, which rejects
managed-Postgres chains. Strip the parameter and verify on the ssl option:

```js
connectionString: (process.env.DATABASE_URL || '').replace(/[?&]sslmode=[^&]*/, ''),
ssl: { rejectUnauthorized: false },
```

### `prisma migrate status` says clean but a column is missing

Migrations using `CREATE TABLE IF NOT EXISTS` no-op against an older-shaped
table of the same name and still report success. Compare
`information_schema.columns` against a known-good environment rather than
trusting migrate status.

### The agent joins but the child hears nothing

Check TTS credentials first, then that the manager API is reachable from the
worker — provider selection comes from the manager and overrides config
defaults, so a manager that is up but returning errors leaves the worker with no
usable TTS.

### `go test ./pkg/livekit/` fails with `libten_vad.so`

A test-loader issue only; the built binary loads it fine. Run tests on a machine
with the library present, or rely on the build.

### `npm ci` fails

`package.json` and `package-lock.json` have disagreed before. Reconcile them in
a commit rather than working around it on a server.

---

## 10. What this document does not cover

- **Firmware and device provisioning** — pairing a physical toy.
- **TLS, domains, reverse proxy** — everything here binds to localhost.
- **`founder-dashboard-web`** — its own `npm install && npm start`.
- **Production hardening** — see [deploy/prod/](../deploy/prod/) and
  [deploy/k8s/capacity-and-hardening.md](../deploy/k8s/capacity-and-hardening.md).
