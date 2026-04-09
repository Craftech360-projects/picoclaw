# Cerebrium Deployment Plan — PicoClaw LiveKit Voice Agent (India)

## Phase 1: Database Setup (STT Providers in PostgreSQL)

Your STT providers are stored in your Supabase PostgreSQL database. This is the source of truth.

### 1.1 Verify the STT table exists

Connect to your Supabase database:
```bash
psql "postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres"
```

Or use Supabase Dashboard → SQL Editor, then run:
```sql
SELECT table_name FROM information_schema.tables
WHERE table_schema = 'public' AND table_name = 'stt_providers';
```

If the table doesn't exist, run the schema:
```bash
psql "postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres" -f scripts/stt_providers_postgres.sql
```

### 1.2 Configure your active STT provider

Pick your primary provider. For India users, recommended options:

**Option A — Deepgram (best quality, 44 languages):**
```sql
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE, api_key = 'sk-your-deepgram-key' WHERE provider_name = 'deepgram';
```

**Option B — Sarvam AI (Indian languages specialist):**
```sql
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE, api_key = 'sk-your-sarvam-key' WHERE provider_name = 'sarvam';
```

**Option C — Groq/Whisper (fastest, good for Indian accents):**
```sql
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE, api_key = 'gsk-your-groq-key' WHERE provider_name = 'groq';
```

Verify:
```sql
SELECT provider_name, model, is_active, 
       CASE WHEN api_key != '' THEN '✓' ELSE '✗' END as key_set
FROM stt_providers WHERE is_active = TRUE;
```

### 1.3 Set API keys for all providers you might use

```sql
-- Set multiple API keys at once
UPDATE stt_providers SET api_key = 'sk-deepgram-key' WHERE provider_name = 'deepgram';
UPDATE stt_providers SET api_key = 'gsk-groq-key' WHERE provider_name = 'groq';
UPDATE stt_providers SET api_key = 'sk-openai-key' WHERE provider_name = 'openai';
UPDATE stt_providers SET api_key = 'sk-sarvam-key' WHERE provider_name = 'sarvam';
```

> **Key point:** Changing `is_active` in the database takes effect on **new sessions only**. Active calls are not interrupted.

---

## Phase 2: How Config Works on Cerebrium (No config.json Needed!)

**Good news: you don't need `config.json` on Cerebrium at all.**

PicoClaw's config loading works like this:
1. `LoadConfig(path)` tries to read `~/.picoclaw/config.json`
2. If the file doesn't exist → it calls `DefaultConfig()` which returns sane defaults
3. The `caarlos0/env` library **overlays environment variables** on top of those defaults

Every config field has an `env:"PICOCLAW_..."` tag. So on Cerebrium, **everything is driven by env vars**.

### 2.1 Required Environment Variables for Cerebrium

These are the **minimum** env vars you need to set in the Cerebrium dashboard:

#### LiveKit Connection (REQUIRED)
| Variable | Example Value | Purpose |
|----------|--------------|---------|
| `PICOCLAW_LIVEKIT_SERVER_URL` | `wss://my-project-abc123.livekit.cloud` | Your LiveKit Cloud WebSocket URL |
| `PICOCLAW_LIVEKIT_API_KEY` | `API_XXXXXXXX` | From LiveKit Cloud dashboard → API Keys |
| `PICOCLAW_LIVEKIT_API_SECRET` | `your_secret_xxxxxxxx` | From LiveKit Cloud dashboard → API Keys |

#### STT (Speech-to-Text)
| Variable | Example Value | Purpose |
|----------|--------------|---------|
| `PICOCLAW_LIVEKIT_STT_DATABASE_URL` | `postgresql://postgres.tsiocyg...` | Your Supabase PostgreSQL URL |
| `PICOCLAW_LIVEKIT_STT_PROVIDER` | `deepgram` | Default provider name (fallback) |
| `PICOCLAW_LIVEKIT_STT_LANGUAGE` | `en-IN` | Indian English; use `hi` for Hindi |
| `DEEPGRAM_API_KEY` | `sk_xxxxxxxx` | Seeds Deepgram into the DB on startup |
| `GROQ_API_KEY` | `gsk_xxxxxxxx` | Seeds Groq into the DB (optional) |
| `SARVAM_API_KEY` | `sk_xxxxxxxx` | Seeds Sarvam into the DB (optional, best for Indian languages) |

#### TTS (Text-to-Speech)
| Variable | Example Value | Purpose |
|----------|--------------|---------|
| `PICOCLAW_LIVEKIT_TTS_PROVIDER` | `elevenlabs` | Your TTS provider |
| `PICOCLAW_LIVEKIT_TTS_VOICE_ID` | `Xb7hH8MSUJpSbSDYk0k2` | ElevenLabs voice ID |
| `PICOCLAW_LIVEKIT_TTS_MODEL_ID` | `eleven_multilingual_v2` | ElevenLabs model |
| `PICOCLAW_LIVEKIT_TTS_FILLER_WORDS` | `["Hmm","Let me think","Okay"]` | Natural filler while LLM thinks |
| `ELEVENLABS_API_KEY` | `sk_xxxxxxxx` | Your ElevenLabs API key |

#### LLM Provider
| Variable | Example Value | Purpose |
|----------|--------------|---------|
| `PICOCLAW_AGENTS_DEFAULTS_PROVIDER` | `openrouter` | Your LLM provider |
| `PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME` | `openrouter` | Model alias from model_list |
| `OPENROUTER_API_KEY` | `sk-or-xxxxxxx` | API key for OpenRouter |

#### Scaling
| Variable | Example Value | Purpose |
|----------|--------------|---------|
| `PICOCLAW_LIVEKIT_MAX_SESSIONS` | `50` | Max concurrent voice sessions per instance |

### 2.2 How it all fits together at runtime

```
┌─────────────────────────────────────────────────────┐
│  Cerebrium Instance boots                           │
│                                                     │
│  1. main.go calls config.LoadConfig("~/.picoclaw/config.json")
│     → File NOT FOUND                                │
│     → Returns DefaultConfig()                       │
│                                                     │
│  2. caarlos0/env overlays all PICOCLAW_* env vars   │
│     → server_url, API keys, STT config, TTS config  │
│     → All defaults are overwritten by your env vars │
│                                                     │
│  3. stt.NewFactory(STT_DATABASE_URL) connects to    │
│     Supabase PostgreSQL → reads active STT provider │
│                                                     │
│  4. STT factory seeds API keys from env vars into   │
│     the database (Deepgram, Groq, Sarvam, etc.)    │
│                                                     │
│  5. Worker connects to LiveKit Cloud via WebSocket  │
│     → Ready to accept voice sessions                │
└─────────────────────────────────────────────────────┘
```

**Key insight:** Your Supabase database is the **source of truth** for STT providers. The env var `DEEPGRAM_API_KEY` seeds it on startup, but the active provider is controlled by running SQL:
```sql
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'deepgram';
```

### 2.3 What about your local config.json?

Your `C:\Users\rahul\.picoclaw\config.json` is **only for local development**. On Cerebrium:
- **Do NOT** copy it into the Docker image (it has hardcoded API keys — security risk)
- **Do NOT** mount it as a volume (unnecessary complexity)
- **Just set env vars** in the Cerebrium dashboard — that's it

If you want to keep a "production config template" for reference, create a clean version **without any API keys**:

```json
{
    "version": 1,
    "agents": {
        "defaults": {
            "workspace": "~/.picoclaw/workspace",
            "restrict_to_workspace": true,
            "allow_read_outside_workspace": false,
            "provider": "openrouter",
            "model_name": "openrouter",
            "max_tokens": 8192,
            "temperature": 0.7,
            "max_tool_iterations": 20,
            "summarize_message_threshold": 0,
            "summarize_token_percent": 0,
            "steering_mode": "one-at-a-time",
            "tool_feedback": { "enabled": false, "max_args_length": 300 },
            "split_on_marker": false
        }
    },
    "livekit_service": {
        "tts": {
            "provider": "elevenlabs",
            "voice_id": "Xb7hH8MSUJpSbSDYk0k2",
            "model_id": "eleven_multilingual_v2",
            "output_format": "pcm_24000",
            "sample_rate_hz": 24000,
            "filler_words": ["Hmm", "Let me think", "Okay", "Sure"]
        },
        "server_url": "",
        "stt": {
            "provider": "deepgram",
            "model": "nova-2",
            "language": "en-IN",
            "database_url": ""
        }
    }
}
```

Save this as `config.prod.template.json` for reference, but **you don't deploy it**.

---

## Phase 3: Build the Docker Image

### 3.1 Create a Dockerfile for Cerebrium

Cerebrium uses your `Dockerfile` directly via `dockerfile_path` in `cerebrium.toml`.

Create `Dockerfile.cerebrium` in the project root:

```dockerfile
# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o picoclaw-livekit ./cmd/picoclaw-livekit

# Runtime stage (minimal)
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/picoclaw-livekit .
COPY prompts/cheeko.tmpl ./prompts/cheeko.tmpl

# No config.json needed — everything comes from env vars!

EXPOSE 8192
CMD ["./picoclaw-livekit", "--agent-name", "picoclaw-voice-agent"]
```

### 3.2 Test locally (with env vars only, no config.json needed)

```bash
# Build the binary for Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o picoclaw-livekit ./cmd/picoclaw-livekit

# Or build the Docker image
docker build -f Dockerfile.cerebrium -t picoclaw-livekit:cerebrium .

# Test locally (all config via env vars, no config.json needed)
docker run --rm \
  -e PICOCLAW_LIVEKIT_SERVER_URL=wss://your-project.livekit.cloud \
  -e PICOCLAW_LIVEKIT_API_KEY=<key> \
  -e PICOCLAW_LIVEKIT_API_SECRET=<secret> \
  -e STT_DATABASE_URL=postgresql://postgres.tsiocyg... \
  -e ELEVENLABS_API_KEY=<key> \
  -e DEEPGRAM_API_KEY=<key> \
  -e OPENROUTER_API_KEY=<key> \
  picoclaw-livekit:cerebrium --agent-name test-agent
```

---

## Phase 4: Deploy to Cerebrium

### 4.1 Create Cerebrium Project

1. Go to [Cerebrium](https://www.cerebrium.ai/) → Create Project
2. Region: **asia-south1 (Mumbai)** — closest to India users
3. Choose GPU/CPU tier based on expected concurrent users:
   - 10-20 users: 4 vCPU, 16 GB RAM
   - 20-50 users: 8 vCPU, 32 GB RAM
   - 50-100 users: 16 vCPU, 64 GB RAM

### 4.2 Set Environment Variables in Cerebrium

All configuration comes from env vars — **no config.json file needed**:

| Variable | Value | Source |
|----------|-------|--------|
| `PICOCLAW_LIVEKIT_SERVER_URL` | `wss://<your-project>.livekit.cloud` | LiveKit Cloud dashboard |
| `PICOCLAW_LIVEKIT_API_KEY` | `<from LiveKit>` | LiveKit Cloud → API Keys |
| `PICOCLAW_LIVEKIT_API_SECRET` | `<from LiveKit>` | LiveKit Cloud → API Keys |
| `PICOCLAW_LIVEKIT_STT_DATABASE_URL` | `postgresql://postgres.tsiocyg...` | Your Supabase connection |
| `PICOCLAW_LIVEKIT_STT_PROVIDER` | `deepgram` | Default fallback provider |
| `PICOCLAW_LIVEKIT_STT_LANGUAGE` | `en-IN` | Indian English |
| `PICOCLAW_LIVEKIT_TTS_PROVIDER` | `elevenlabs` | Your TTS provider |
| `PICOCLAW_LIVEKIT_TTS_VOICE_ID` | `Xb7hH8MSUJpSbSDYk0k2` | ElevenLabs voice |
| `PICOCLAW_LIVEKIT_TTS_MODEL_ID` | `eleven_multilingual_v2` | ElevenLabs model |
| `PICOCLAW_AGENTS_DEFAULTS_PROVIDER` | `openrouter` | Your LLM provider |
| `PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME` | `openrouter` | Model alias |
| `PICOCLAW_LIVEKIT_MAX_SESSIONS` | `50` | Per-instance limit |
| `DEEPGRAM_API_KEY` | `sk_...` | Deepgram dashboard |
| `ELEVENLABS_API_KEY` | `sk_...` | ElevenLabs dashboard |
| `OPENROUTER_API_KEY` | `sk-or-...` | OpenRouter dashboard |
| `GROQ_API_KEY` | `gsk_...` | Groq dashboard (optional) |
| `SARVAM_API_KEY` | `sk_...` | Sarvam dashboard (optional) |

### 4.3 Configure Cerebrium Deployment

Create `cerebrium.toml` in project root:

```toml
[cerebrium]
name = "picoclaw-voice-agent"
region = "asia-south1"  # Mumbai — closest to India users

[cerebrium.compute]
gpu = "None"   # CPU-only — voice processing doesn't need GPU
cpu = 8        # vCPUs per replica
memory = 32    # GB RAM per replica

[cerebrium.scaling]
min_replicas = 1            # keep 1 alive always (no cold start for voice)
max_replicas = 5            # auto-scale up to this
cooldown = 30               # seconds between scaling actions
replica_concurrency = 1     # 1 live session per worker slot
response_grace_period = 900 # wait 15 min before terminating idle replica
scaling_metric = "cpu_utilization"  # CPU-based, not HTTP queue
scaling_target = 80         # scale up when CPU hits 80%

[cerebrium.runtime.custom]
port = 8192
dockerfile_path = "./Dockerfile.cerebrium"
healthcheck_endpoint = "/health"
readycheck_endpoint = "/ready"

[cerebrium.secrets]
PICOCLAW_LIVEKIT_SERVER_URL = true
PICOCLAW_LIVEKIT_API_KEY = true
PICOCLAW_LIVEKIT_API_SECRET = true
PICOCLAW_LIVEKIT_STT_DATABASE_URL = true
PICOCLAW_LIVEKIT_STT_PROVIDER = false
PICOCLAW_LIVEKIT_STT_LANGUAGE = false
PICOCLAW_LIVEKIT_TTS_PROVIDER = false
PICOCLAW_LIVEKIT_TTS_VOICE_ID = false
PICOCLAW_LIVEKIT_TTS_MODEL_ID = false
PICOCLAW_AGENTS_DEFAULTS_PROVIDER = false
PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME = false
PICOCLAW_LIVEKIT_MAX_SESSIONS = false
DEEPGRAM_API_KEY = true
ELEVENLABS_API_KEY = true
OPENROUTER_API_KEY = true
GROQ_API_KEY = true
SARVAM_API_KEY = true
```

> **Note:** Check [Cerebrium's latest docs](https://cerebrium.ai/docs) for the exact `cerebrium.toml` syntax — the format evolves. The key fields are `scaling`, `compute`, `runtime.custom` with `dockerfile_path`, and `secrets`.

### 4.4 Deploy

```bash
# Install Cerebrium CLI
pip install cerebrium

# Login
cerebrium login

# Deploy
cerebrium deploy
```

### 4.5 ⚠️ Cerebrium Auto-Scaling for LiveKit Workers — The Correct Approach

**Important correction:** Cerebrium **does** support auto-scaling for LiveKit workers — just not via HTTP queue depth. Instead, it uses **CPU utilization** as the scaling metric.

From Cerebrium's own LiveKit voice agent deployments, the scaling config looks like this:

```toml
[cerebrium.scaling]
min_replicas = 1
max_replicas = 5
cooldown = 30
replica_concurrency = 1
response_grace_period = 900
scaling_metric = "cpu_utilization"
scaling_target = 80
```

**How it works:**
- `scaling_metric = "cpu_utilization"` — Cerebrium monitors CPU usage, not HTTP queue depth
- `scaling_target = 80` — when CPU hits 80%, Cerebrium spins up a new replica
- `replica_concurrency = 1` — one voice session per replica (each session needs full resources)
- `min_replicas = 1` — always keep one worker alive (no cold start for voice)
- `max_replicas = 5` — auto-scale up to 5 replicas
- `response_grace_period = 900` — wait 15 minutes before terminating an idle replica

### How Many Sessions Per Replica?

This depends on your CPU/memory allocation and STT/TTS provider. Based on Cerebrium's benchmarks:

| Resource per Replica | Sessions per Replica | Good for |
|---------------------|---------------------|----------|
| 2 vCPU, 12 GB RAM   | ~1-2 (light LLM usage) | Small scale |
| 4 vCPU, 16 GB RAM   | ~5-10 sessions      | Moderate |
| 8 vCPU, 32 GB RAM   | ~20-50 sessions     | Production |

For PicoClaw with `PICOCLAW_LIVEKIT_MAX_SESSIONS=50` per replica, you'd want **8 vCPU, 32 GB RAM** per replica.

### Total Capacity with Auto-Scaling

| min_replicas | max_replicas | Sessions/Replica | Peak Capacity |
|-------------|-------------|-----------------|---------------|
| 1           | 3           | 50              | 150           |
| 1           | 5           | 50              | 250           |
| 1           | 10          | 50              | 500           |

**Important:** CPU-based scaling works because more concurrent voice sessions = more CPU usage (STT processing, LLM calls, TTS synthesis). When CPU spikes, Cerebrium adds replicas.

### Why This Works for PicoClaw

Each voice session on a replica consumes:
- **STT**: Deepgram WebSocket streaming (moderate CPU)
- **LLM**: Context building + tool execution (CPU + RAM)
- **TTS**: Cartesia/ElevenLabs streaming (moderate CPU)
- **VAD**: TEN VAD engine (light CPU)

When you approach `maxSessions=50`, CPU will hit the `scaling_target=80` threshold → Cerebrium spins up a new replica → it registers with LiveKit → LiveKit routes new rooms to it.

---

## Phase 5: Get LiveKit Cloud Credentials

### 5.1 Create LiveKit Project

1. Go to [livekit.io](https://livekit.io/) → Sign up
2. Create a new project
3. Choose region: **Mumbai (ap-south-1)** or **Singapore (ap-southeast-1)**
4. Note your:
   - Project URL: `wss://<project>.livekit.cloud`
   - API Key
   - API Secret

### 5.2 Configure Agent Access

In LiveKit Cloud dashboard:
1. Go to **Agents** → **Create Agent**
2. Upload your Docker image or provide the image URL
3. Set the environment variables (same as Phase 4.2)
4. Set `agent_name` to match your `--agent-name` flag

---

## Phase 6: Verify & Monitor

### 6.1 Test a voice call

```bash
# Use LiveKit CLI or a test client to join a room
npx @livekit/cli join-room --room test-room --identity test-user
```

### 6.2 Check logs

```bash
# Cerebrium logs
cerebrium logs

# Look for:
# "STT factory initialized  db_url=... providers=[...]"
# "Using STT provider  provider=deepgram"
# "Successfully injected zero-latency dynamic IDENTITY.md"
```

### 6.3 Monitor active sessions

Check your Supabase database for active providers:
```sql
SELECT provider_name, model, is_active, updated_at
FROM stt_providers WHERE is_active = TRUE;
```

### 6.4 Scaling triggers

Monitor these metrics to know when to scale:
- **Concurrent sessions per instance** → if hitting 70% of `maxSessions`, add another instance
- **Response latency** → if LLM response > 2s, upgrade instance CPU
- **STT error rate** → if transcription fails spike, switch STT provider via SQL

---

## Quick Reference: Switching STT Provider at Runtime

No redeployment needed — just run SQL on your Supabase database:

```sql
-- Switch to Groq (fastest)
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'groq';

-- Switch to Sarvam (best for Indian languages)
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'sarvam';

-- Switch to Deepgram (best quality)
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'deepgram';
```

New voice sessions will use the new provider immediately.

---

## Summary Checklist

- [ ] STT table created in Supabase (`stt_providers_postgres.sql`)
- [ ] Active STT provider set with API key in database
- [ ] Dockerfile created (`Dockerfile.cerebrium`) — **no config.json included**
- [ ] `cerebrium.toml` configured with CPU-based auto-scaling (`scaling_metric = "cpu_utilization"`)
- [ ] LiveKit Cloud project created (Mumbai/Singapore region)
- [ ] Cerebrium project created (asia-south1 region, Mumbai)
- [ ] All environment variables set in Cerebrium (LiveKit URL, API keys, STT DB URL)
- [ ] First instance deployed
- [ ] Voice call tested end-to-end
- [ ] Auto-scaling verified (monitor CPU metrics, add load, verify new replicas spin up)
- [ ] Monitoring set up (logs, active providers, session count)
