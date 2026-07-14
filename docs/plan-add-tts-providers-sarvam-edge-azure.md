# Plan: Add 3 TTS providers — Sarvam v3, Edge TTS, Azure TTS

Status: **awaiting approval** · Scope: full working clients · Repos: picoclaw (Go) + manager-api-node (data only)

## Goal

Add three selectable TTS providers to the LiveKit voice worker, driven by the
existing manager-api `/livekit/providers/active` mechanism:

- **Sarvam v3** (`bulbul:v3`) — Indian-language REST TTS.
- **Edge TTS** — free, keyless Microsoft endpoint; the cheap developer path.
- **Azure TTS** — Azure Cognitive Services Speech (paid, production-grade).

Each must implement `tts.Provider.Synthesize(ctx, text) (AudioStream, error)`
returning raw PCM chunks at the declared sample rate. The pipeline resamples to
48 kHz.

## Key findings (why this is mostly a picoclaw change)

1. **manager-api-node is generic.** `tts_providers` is a plain table keyed by a
   unique `provider_name`; `livekitProviders.service.js` maps any row to
   `/providers/active` with no allowlist/enum. Adding providers there is **pure
   data** — seed rows + activation. No JS/Prisma code changes.
2. **Selection flow already exists.** `resolveLiveKitProviderConfigForSession`
   fetches active providers (30s cache), and `applyManagerTTSProvider`
   (`cmd/picoclaw-livekit/manager_provider_runtime.go:235`) overlays the TTS row
   onto the session config and routes the API key by provider name. New
   providers slot into the existing `switch`.
3. **Registration is one line each** in `buildTTSProvider`
   (`cmd/picoclaw-livekit/main.go:1483`).
4. **Two of three are batch, not streaming.** Sarvam and Azure REST return a
   full buffer; their `AudioStream` yields it once then `io.EOF`. Edge is a real
   websocket stream (like the existing `smallest_tts`).
5. **Session language already resolved.** Room metadata carries
   `session_language_code` (`cmd/picoclaw-livekit/bootstrap_metadata.go:30`),
   derived upstream from the child profile. Sarvam needs this; no USER.md
   parsing and no DB language column required.

## Provider client designs

All new packages mirror `pkg/voice/smallest_tts/` layout:
`types.go` + `tts.go` + `builder.go` + `tts_test.go`.

### 1. `pkg/voice/sarvam_tts/` (REST, batch)

- Endpoint: `POST https://api.sarvam.ai/v1/tts`
- Auth: `Authorization: Bearer <api_key>` (from DB `api_key`, env fallback `SARVAM_API_KEY`)
- Body:
  ```json
  {
    "model": "bulbul:v3",
    "text": "<text>",
    "voice": "<voice_id>",
    "language_code": "<resolved>",
    "sample_rate": <sample_rate_hz>,
    "enable_preprocessing": true
  }
  ```
  - `model` ← DB `model_id` (default `bulbul:v3`)
  - `voice` ← DB `voice_id` (default `meera`)
  - `sample_rate` ← DB `sample_rate_hz` (default 22050)
- Response handling (defensive — shape confirmed against a live call during impl):
  decode base64 WAV from `audios[]` (or `audio`), strip 44-byte WAV header → one
  PCM chunk; if body is raw audio, pass through.
- **Language resolution** (Sarvam only) on the session language code:
  ```
  code = session_language_code
  lang = base(code)                       // "hi-IN" -> "hi"
  if code == "" or lang in {en, hi}: "hi-IN"
  else if "<lang>-IN" in supported:  "<lang>-IN"
  else:                               "hi-IN"
  supported = hi-IN en-IN bn-IN gu-IN kn-IN ml-IN mr-IN od-IN pa-IN ta-IN te-IN
  ```

### 2. `pkg/voice/edge_tts/` (websocket, streaming, keyless)

- Endpoint: `wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1?TrustedClientToken=6A5AA1D4EAFF4E9FB37E23D68491D6F4`
- Auth: none. Must send the `Sec-MS-GEC` token (SHA-256 of trusted-token +
  Windows-filetime timestamp rounded to 5 min) + `Sec-MS-GEC-Version` header, as
  Microsoft now requires.
- Protocol: send `speech.config` JSON message, then an SSML `ssml` message with
  `X-Microsoft-OutputFormat: raw-24khz-16bit-mono-pcm`; read binary frames,
  strip the `Path:audio\r\n...\r\n\r\n` header prefix → PCM; text frame with
  `Path:turn.end` → `io.EOF`.
- `voice` ← DB `voice_id` (default `en-US-AnaNeural`, a child-friendly voice).
- Reuses `github.com/gorilla/websocket` (already a dep).
- **Note:** unofficial and fragile; documented in code as dev-only.

### 3. `pkg/voice/azure_tts/` (REST, batch)

- Endpoint: `POST https://<region>.tts.speech.microsoft.com/cognitiveservices/v1`
  - region ← env `AZURE_SPEECH_REGION` (e.g. `eastus`); optional full override
    `AZURE_SPEECH_ENDPOINT`.
- Auth: `Ocp-Apim-Subscription-Key: <api_key>` (from DB `api_key`, env fallback
  `AZURE_SPEECH_KEY`).
- Headers: `X-Microsoft-OutputFormat: raw-24khz-16bit-mono-pcm`,
  `Content-Type: application/ssml+xml`.
- Body: SSML with `voice name` ← DB `voice_id` (default `en-US-AnaNeural`).
- Response: headerless PCM → one chunk then `io.EOF`.

## Shared plumbing (picoclaw)

1. **`cmd/picoclaw-livekit/main.go`** `buildTTSProvider` — register:
   `"sarvam"`, `"edge"`, `"edgetts"`, `"azure"`.
2. **`cmd/picoclaw-livekit/manager_provider_runtime.go`** `applyManagerTTSProvider`
   key `switch` — add cases: `sarvam` → `SetSarvamAPIKey`; `azure` →
   `SetAzureAPIKey`; `edge`/`edgetts` → no key.
3. **`pkg/config/config.go`** — add `sarvamAPIKey`, `azureAPIKey` fields +
   getters/setters + secrets load (mirror `smallestAPIKey` at lines ~923, ~938,
   ~1698). Add in-memory `Language` field to `LiveKitServiceTTSConfig` (NOT a DB
   column) — set per session, read only by Sarvam.
4. **Session language wiring** — populate the cloned session config's
   `TTS.Language` from `SessionLanguageCode` where the per-session TTS provider
   is built (`main.go` bridge path, around line 378). Other providers ignore it.
5. **`scripts/tts_providers_postgres.sql`** — 3 seed rows (`sarvam`, `edge`,
   `azure`), `is_active=false`, blank keys, sensible defaults
   (model_id/voice_id/sample_rate).

## manager-api-node (data only, no code)

- Add the same 3 rows to `database-setup.sql` (and/or the seed script) matching
  the generic pattern. Operator activates one via existing
  `PUT /toy/livekit/providers/active/tts` or admin dashboard.
- If the admin dashboard has a hardcoded provider dropdown, add the 3 names
  there (UI-only). To be verified when we reach it — not expected to block.

## Environment variables (new)

| Var | Provider | Purpose | Default |
|-----|----------|---------|---------|
| `SARVAM_API_KEY` | Sarvam | key fallback if DB blank | — |
| `AZURE_SPEECH_KEY` | Azure | key fallback if DB blank | — |
| `AZURE_SPEECH_REGION` | Azure | region for endpoint host | — (required for Azure) |
| `AZURE_SPEECH_ENDPOINT` | Azure | full endpoint override | derived from region |

Edge TTS needs no env.

## Tests (offline, no network)

- `pkg/voice/sarvam_tts/tts_test.go` — `httptest` server returns a known base64
  WAV; assert PCM bytes + header stripped; assert request body fields.
- `pkg/voice/sarvam_tts/lang_test.go` — table test for the language resolver
  (`""→hi-IN`, `en-IN→hi-IN`, `hi-IN→hi-IN`, `ta-IN→ta-IN`, `de-DE→hi-IN`).
- `pkg/voice/azure_tts/tts_test.go` — `httptest` returns raw PCM; assert
  passthrough + region/endpoint construction + headers.
- `pkg/voice/edge_tts/tts_test.go` — unit-test the frame parser (strip
  `Path:audio` prefix) and `Sec-MS-GEC` token generation against a fixed
  timestamp; no live socket.
- Extend `cmd/picoclaw-livekit/tts_provider_test.go` — assert each new name
  builds a non-nil provider with the right sample rate.
- Live tests (real keys) gated behind env, mirroring
  `pkg/voice/smallest_tts/live_test.go`.

## Verification

- `go build ./...` and `go test ./pkg/voice/... ./cmd/picoclaw-livekit/...`.
- Manual: seed + activate each provider row, run a session, confirm audio.

## Out of scope

- No DB language column (session code + env cover it).
- No manager-api business-logic changes (table is generic).
- No streaming for Sarvam/Azure (batch is sufficient for turn-based TTS).
- Admin dashboard restyling.

## Open items to confirm at implementation

1. Sarvam `/v1/tts` **response JSON shape** (`audios[]` vs `audio` vs binary) —
   handled defensively, confirmed against a live call.
2. Edge `Sec-MS-GEC` token scheme — implement current known algorithm; if MS has
   changed it, Edge may need a follow-up.
3. Default child-friendly voice IDs per provider (seed values).
