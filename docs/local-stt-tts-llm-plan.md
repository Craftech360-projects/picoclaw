# Local STT / TTS / LLM Implementation Plan

> Goal: run picoclaw's LiveKit voice agent fully offline (no cloud APIs) by pointing
> STT, TTS, and LLM at local inference servers. **Order: STT → TTS → LLM** (LLM only
> after STT+TTS proven working).

## Strategy

One server covers STT **and** TTS: **[speaches](https://github.com/speaches-ai/speaches)**
(formerly `faster-whisper-server`, MIT). It exposes OpenAI-compatible endpoints:

- STT → `POST /v1/audio/transcriptions` (faster-whisper)
- TTS → `POST /v1/audio/speech` (Kokoro / Piper)

LLM stays on **Ollama** (already supported, OpenAI-compatible). Run both on a GPU PC;
point picoclaw at its LAN IP. picoclaw does **no** local inference — it is only a client.

```
Laptop (picoclaw LiveKit agent)  ──►  GPU PC
   audio loop only                     ├─ speaches  : STT + TTS  (port 8000)
                                       └─ ollama    : LLM        (port 11434)
```

---

## Phase 0 — Stand up speaches  ✅ DONE (this PC, CPU)

Running and verified on `localhost:8000` (no NVIDIA GPU here → CPU image):
```bash
docker run --detach --restart unless-stopped --publish 8000:8000 --name speaches \
  --volume hf-hub-cache:/home/ubuntu/.cache/huggingface/hub \
  ghcr.io/speaches-ai/speaches:latest-cpu
# download models via API:
curl -X POST http://localhost:8000/v1/models/Systran/faster-whisper-small
curl -X POST http://localhost:8000/v1/models/speaches-ai/Kokoro-82M-v1.0-ONNX
```
- STT model: `Systran/faster-whisper-small`
- TTS model: `speaches-ai/Kokoro-82M-v1.0-ONNX`, voice `af_heart`
- Round-trip TTS→STT verified; Kokoro PCM = **24000 Hz** (matches picoclaw default).

### Activate in DB (Phases 1 & 2)
- Worker env: `OPENAI_BASE_URL=http://localhost:8000/v1` (or this PC's LAN IP if the
  worker runs elsewhere).
- `stt_providers`: activate `openai`, `model=Systran/faster-whisper-small`,
  `api_key=local` (non-empty).
- `tts_providers`: activate `openai`, `voice_id=af_heart`,
  `model_id=speaches-ai/Kokoro-82M-v1.0-ONNX`, `sample_rate_hz=24000`.

---

### (original Phase 0 notes)

1. On the GPU PC: `docker compose -f compose.cuda.yaml up -d` (or `compose.cpu.yaml`).
2. Pull models (whisper + a Piper/Kokoro voice) per speaches docs.
3. Verify from the laptop:
   - STT: `curl -F file=@sample.wav -F model=Systran/faster-whisper-small http://<GPU_IP>:8000/v1/audio/transcriptions`
   - TTS: `curl http://<GPU_IP>:8000/v1/audio/speech -d '{"model":"...","input":"hello","voice":"..."}' --output out.wav`

**Gate:** both curls return valid output before touching Go code.

---

## Phase 1 — STT  ✅ CODE DONE

**What was done:** the `openai` STT provider is OpenAI-compatible (buffers PCM → WAV →
`/v1/audio/transcriptions`) but hardcoded `api.openai.com`. Added an `OPENAI_BASE_URL`
env override ([openai_provider.go](../pkg/voice/stt/openai_provider.go), `newOpenAISTTClient`).
No Prisma / Manager-API change needed for STT — the env var carries the base URL.

**To activate (no code):**
1. On the picoclaw worker, set `OPENAI_BASE_URL=http://<GPU_IP>:8000/v1`.
2. In the `stt_providers` DB table: set `openai` row `is_active = true`,
   `model = <speaches whisper id, e.g. Systran/faster-whisper-small>`,
   `api_key = anything-non-empty` (speaches ignores it, but the provider rejects empty).
3. Restart the worker; speak in a room; confirm transcript in `livekit` logs and **no**
   traffic to `api.openai.com`.

**Note:** this is the batch path (`SupportsStreaming: false`) — expect a beat of latency
after the user stops talking. Fine for v1.

---

### (original notes)

picoclaw has two OpenAI-compatible STT paths. Pick the lower-effort one **after a 10-min check**:

- **Option A (preferred): `openai` STT provider** — already registered
  ([factory.go:315](../pkg/voice/stt/openai_provider.go)). Streaming-capable, speaks
  `/v1/audio/transcriptions`.
- **Option B (fallback): Groq transcriber** — batch/file path, OpenAI-compatible
  ([groq_transcriber.go:28,91](../pkg/voice/groq_transcriber.go)). Higher latency
  (transcribes after utterance ends).

### Steps
1. **Check** `pkg/voice/stt/openai_provider.go`: does it already have a configurable
   base URL? The DB schema has a `config_json` JSONB column
   ([factory.go:261](../pkg/voice/stt/factory.go#L261)) but `GetActiveProvider` only
   reads `provider_name, api_key, model` ([factory.go:88](../pkg/voice/stt/factory.go#L88)).
   - If the provider hard-codes `api.openai.com`, add a base-URL override (env var or
     read `config_json`). **Smallest change wins.**
2. Activate the local STT provider in the `stt_providers` DB table:
   - `provider_name` = `openai` (or whichever), `api_key` = anything non-empty,
     `model` = the speaches whisper model id, `is_active = TRUE`.
3. Point its base URL at `http://<GPU_IP>:8000/v1`.

### Verify
- Speak into a room; confirm transcript appears (check `livekit` logs).
- Confirm **no** outbound traffic to `api.deepgram.com` / `api.openai.com`.

**Risk:** if the `openai` STT provider is a streaming WS client (not file POST), it
won't match speaches' batch endpoint → use Option B (Groq transcriber) instead.

---

## Phase 2 — TTS  ✅ CODE DONE

**What was done:** new `pkg/voice/openai_tts/` provider (POST `/audio/speech`,
`response_format: pcm`, streams raw PCM) modeled on `cartesia_tts`. Registered as
`openai` in `buildTTSProvider` ([main.go:1337](../cmd/picoclaw-livekit/main.go#L1337)).
Base URL from `OPENAI_BASE_URL` (same env as STT → one server for both). Builds + vets
clean; full `cmd` build needs a C compiler for the `vad` cgo package (pre-existing).

**To activate (no code):**
1. `OPENAI_BASE_URL` already set from Phase 1 (shared).
2. In `tts_providers` DB table: set `openai` row `is_active = true`,
   `voice_id = <speaches voice>`, `model_id = <speaches tts model>`,
   `sample_rate_hz = 24000`. The Manager API already forwards `tts.provider` →
   `cfg.LiveKitService.TTS.Provider`, so no Manager/Prisma change needed.
3. Restart; agent should speak via speaches.

**⚠️ Verify sample rate:** we request OpenAI `pcm` (24kHz s16le mono). Confirm speaches
returns exactly that; if it returns a different rate, set `sample_rate_hz` to match or
the audio will sound wrong (chipmunk/slow). This is the one real integration unknown.

---

### (original notes)

No OpenAI-TTS provider exists in picoclaw today (registered: elevenlabs, inworld,
cartesia, deepgram — [main.go:1333-1336](../cmd/picoclaw-livekit/main.go#L1333)).

### Steps
1. **New provider** `pkg/voice/openai_tts/` (model on `cartesia_tts/` — simplest
   existing HTTP provider):
   - `POST {base}/v1/audio/speech` with `{model, input, voice, response_format}`.
   - Return PCM stream matching picoclaw's `tts.AudioStream` interface.
2. **Register it**: add `factory.Register("openai", openai_tts.NewBuilder())` in
   `buildTTSProvider` ([main.go:1331](../cmd/picoclaw-livekit/main.go#L1331)).
3. Configure base URL → `http://<GPU_IP>:8000/v1`, set TTS provider = `openai`.

### Verify
- Agent speaks; audio is heard in the room.
- **Confirm sample rate** of speaches output matches the pipeline's expected rate
  (picoclaw uses 22050/24000 depending on provider). Request the matching
  `response_format` / sample rate, or resample. ← the one real integration unknown.

---

## Phase 3 — LLM (only after STT+TTS work)

Zero new code. Set the LLM to local Ollama via your existing path.

1. On GPU PC: `ollama pull qwen3:4b` (laptop-friendly) or a larger model if the GPU
   allows. Set `OLLAMA_HOST=0.0.0.0:11434`, restart, open firewall port 11434.
2. In your **Manager API** `/livekit/providers/active` response set:
   ```json
   { "llm": { "model_name": "remote-llm", "model": "ollama/qwen3:4b",
              "api_base": "http://<GPU_IP>:11434/v1", "api_key": "" } }
   ```
   `api_base` already flows through ([manager_provider_runtime.go:208](../cmd/picoclaw-livekit/manager_provider_runtime.go#L208)).
3. **Thinking output:** Qwen3 uses `<think>` tags → already stripped before TTS
   ([audio_pipeline.go:27](../pkg/livekit/audio_pipeline.go#L27)). To save tokens,
   disable with `/no_think` or a no-think Ollama Modelfile variant.

### Verify
- Full loop: speak → transcript → Ollama reply → spoken response, all offline.

---

## Manager-API wiring — `api_base` for STT/TTS (end-to-end)

You load providers via the Manager API at
`D:\cheeko-backend\main\manager-api-node`, DB = Supabase Postgres. Current state:

| Provider | DB `api_base`? | Endpoint serves it? | picoclaw reads it? |
|----------|---------------|---------------------|--------------------|
| **LLM**  | ✅ `api_base` column | ✅ yes | ✅ yes — **works today** |
| **STT**  | ❌ (has `config_json` JSONB) | ❌ | ❌ |
| **TTS**  | ❌ (has `config_json` JSONB) | ❌ | ❌ |

So LLM (Phase 3) needs **zero** changes — just set `api_base` in the `llm_providers`
row. STT/TTS need `api_base` plumbed through 3 layers:

**1. DB** (`prisma/schema.prisma`): add `api_base String?` to `stt_providers`
(lines ~1207) and `tts_providers` (lines ~1224), then `prisma migrate`. *(Lazy
alt: stuff `{"api_base": "..."}` into the existing `config_json` and skip the
migration — but a real column matches `llm_providers` and avoids JSON parsing in
3 places. Prefer the column.)*

**2. Manager API service**
(`src/services/livekitProviders.service.js`, `getActiveProviders()` ~lines 195-218):
add `api_base` to the `stt` and `tts` response objects.

**3. picoclaw** (`cmd/picoclaw-livekit/manager_provider_runtime.go`):
- add `APIBase string \`json:"api_base"\`` to `managerActiveSTTProvider` (line 32)
  and `managerActiveTTSProvider` (line 38)
- thread into `cfg.LiveKitService.STT.*` / `cfg.LiveKitService.TTS.*` in
  `applyManagerSTTProvider` (line 218) / `applyManagerTTSProvider` (line 233)
- ensure the STT/TTS providers actually consume that base URL (Phase 1/2 code)

**Selection:** all three tables use `is_active = true` + `priority DESC`. Activating
one deactivates the rest in a transaction. To switch to local: insert/activate a row
with the local `api_base`.

---

## Open questions to resolve during implementation
1. Is `openai` STT provider file-POST or streaming-WS? (decides Phase 1 option)
2. speaches TTS output sample rate / format vs picoclaw pipeline expectation.
3. Does STT config flow through Manager API or startup config in your deployment?
4. GPU on the inference PC — decides Whisper model size and LLM size.

## Out of scope (ponytail: add only if needed)
- Streaming STT (Deepgram-protocol emulation) — batch is fine for v1.
- ElevenLabs WS local shim — OpenAI `/v1/audio/speech` is simpler.
- Per-session provider switching beyond what Manager API already does.
