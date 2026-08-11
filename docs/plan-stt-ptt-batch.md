# Plan — PTT-gated batch STT (Manual Talk, no VAD)

> Branch `stt-ptt-batch` in **picoclaw** (off `stt-ten-vad-only`) and **cheeko-backend** (off `main`).
> Status: APPROVED DESIGN (grilled 2026-08-11) — see ADR 0007 for the decision record.
> Prior art: `docs/sarvam-stt-vad-findings.md`. Glossary: `CONTEXT.md` (Manual Talk, Turn Boundary, End Turn, Cancel Turn).

## Goal

In Manual Talk the knob gives ground-truth turn boundaries, so no VAD — device-side or
server-side — has any job. Buffer the utterance between tap-1 and tap-2, then one Sarvam
REST call. Expected: transcript ~1–2s after tap-2 (vs `stt_first_final_ms=12062`
streaming), batch-quality output, kid's language auto-detected.

**Scope (grilled):** Manual Talk / Talk-card sessions only. client.py proves it end-to-end
first; firmware unchanged until then. Streaming+VAD path stays intact for auto/wake-word
modes — worker-level provider flip chooses the path. Realtime mode explicitly unsupported
by this provider (no boundary events; would degrade to 25s turns).

## Flow

```
tap 1        listen/start ──▶ gateway ──ptt_event press──▶ agent: ResetBuffer + synthetic SpeechStart
talking      device audio ──UDP──▶ gateway ──LiveKit track──▶ agent: SendAudio → append to buffer
tap 2        speech_end ──▶ gateway ──speech_end──▶ agent: +200ms grace → synthetic SpeechEnd
                                                    └─▶ Finalize → WAV → POST Sarvam REST → transcript.final → LLM
double-click listen/stop ──▶ gateway ──ptt_event release──▶ agent: ResetBuffer + synthetic SpeechEnd (Cancel Turn: silent no-op)
```

## Signal semantics (grilled, Q2)

| wire signal | meaning | agent action |
|---|---|---|
| `ptt_event` press (`listen/start`) | mic on, new utterance | `ResetBuffer()` + inject `VADEvent{SpeechStart}` |
| `speech_end` | **End Turn** — process | 200ms grace → inject `VADEvent{SpeechEnd}` → RunInbound calls `Finalize()` |
| `ptt_event` release (`listen/stop`) | **Cancel Turn** — discard | `ResetBuffer()` + inject `VADEvent{SpeechEnd}` → Finalize on empty buffer = silent no-op |

Ignoring release would let the 25s segment cap transcribe cancelled audio and answer
unprompted — release MUST discard.

## Sarvam REST call (grilled, Q4)

`POST https://api.sarvam.ai/speech-to-text`, header `api-subscription-key`, multipart:
- `file` — WAV (16kHz mono PCM16; `createWAVFromPCM` helper already in repo)
- `model` — **`saaras:v4`** (latest; from manager `ProviderInfo.Model`, code stays model-agnostic)
- `language_code` — **`unknown`** (auto-detect; kid speaks any of 22 languages + English)
- `mode=transcribe` — docs say v3-only but transcribe is the default anyway; **smoke test
  decides**: send it, drop the field if v4 rejects it
- Response: `transcript` + detected `language_code` (+ `language_probability`) →
  `TranscriptEvent{Text, IsFinal: true, Language: language_code}`

REST endpoint is explicitly "<30 seconds" — fits the 25s segment cap + 30s buffer cap.
(Sarvam's separate "Batch API" is a different async product — hence provider name
`sarvam_rest`, NOT `sarvam_batch`.)

## Repo 1 — cheeko-backend (gateway: ZERO changes)

`listen`→`ptt_event` forwarding (`mqtt/virtual-connection.js:1493`) and `speech_end`
forwarding (`:1675`) already exist.

| # | change | file | size |
|---|--------|------|------|
| C1 | ✅ done: `_send_ptt()` — `listen/start` on mic open, `speech_end` on recording end | `client.py` | done |
| C2 | Keybind `s` in the existing key-monitor thread → `stop_recording_event.set()` — tester ends the turn locally | `client.py` | ~8 lines |

## Repo 2 — picoclaw

### P0 — smoke test (before any wiring)

Run the REST call (curl or tiny Go test) against captured child WAVs
(gateway `deviceAudioRecorder` output) with `saaras:v4` + `unknown`: confirms transcript
quality, the `mode` param question, latency on ~5–10s clips, and the API key works.

### P1 — `pkg/voice/stt/sarvam_rest_provider.go` (new, ~180 lines)

Copy `groqStreamAdapter` shape (`groq_provider.go:83`):
- `SendAudio` → append to buffer, cap 30s (960KB), WARN once per segment past cap.
- `Finalize()` → empty buffer: silent no-op (Cancel Turn). Else: **goroutine** (never
  block RunInbound; groq does this sync — known flaw, don't copy): WAV → POST (10s ctx
  timeout, one retry) → emit final event; clear buffer.
- Buffer had real audio (>0.5s) but transcript came back empty/blank → **fire
  empty-result callback** instead of emitting (grilled Q3).
- `ResetBuffer()` — extra method, type-asserted by room_session.
- `SetEmptyResultHandler(func())` — same type-assert pattern.
- Register `"sarvam_rest"` in `factory.go`; key/model/language flow via existing
  `ProviderInfo` — no new config plumbing.

REST fails twice → emit nothing → existing finalize-timeout clean reset.

### P2 — `pkg/livekit/room_session.go` wiring (~60 lines)

1. Data-channel switch (`:380`) new cases per the semantics table above — synthetic
   events go into the existing `vadEvent` channel, so **RunInbound is untouched**
   (its SpeechEnd arm already calls `Finalize()`; 25s hard cap is a free backstop).
2. Empty-result handler → enqueue canned announcement via existing
   `enqueueAnnouncement` machinery: **"I didn't hear you! Press the button and try
   again."** (English, v1) → TTS plays → firmware unlocks cleanly instead of freezing
   40s in "Thinking…". Cancel stays silent (empty buffer ≠ empty result).
3. When active provider is `sarvam_rest`: don't construct TEN VAD
   (`sttStreamWriter.vad = nil`) — PTT events are the only turn signal.

### P3 — activation (grilled Q4: manager, NOT config.json)

Worker runs in manager-API mode; STT provider comes from `/livekit/providers/active`
on a TTL tick (`stt_manager_bootstrap.go`). Activate: flip manager's active STT to
`sarvam_rest` (model `saaras:v4`, language `unknown`, same Sarvam key). Rollback: flip
back to `sarvam`. Mid-session flips lose the in-flight buffer — accepted, flip between
sessions.

### P4 — tests (~150 lines)

- Provider (httptest): happy path buffer→WAV→POST→final; empty-buffer no-op; real-audio→
  empty-transcript fires callback not event; cap; retry-then-fail emits nothing;
  ResetBuffer discards.
- room_session: press/speech_end/release data messages → correct injections +
  ResetBuffer/announcement calls (existing fake stream pattern).

## Order & verify

| step | verify |
|------|--------|
| 0. P0 smoke test on captured WAVs | transcript quality + mode-param answer + latency |
| 1. P1 provider + tests | `go test ./pkg/voice/stt/ -run SarvamRest` |
| 2. P2 wiring + tests | `go test ./pkg/livekit/` |
| 3. C2 keybind | syntax + manual run |
| 4. Dev box deploy (`stt-ptt-batch`), manager flip → `sarvam_rest` | client.py session: talk, press `s` → transcript 1–2s, correct language; press `s` silent → "didn't hear" prompt; `stt_first_final_ms` logged |
| 5. Real device (Talk card) | same from the knob; double-click cancel stays silent |

Estimate: ~420 lines incl. tests. One deploy cycle. Firmware phase starts only after
step 4 passes.
