# Sarvam STT + VAD — Findings and Current Configuration

Investigation of 2026-08-10 into why children's speech produced no usable transcript
on the LiveKit voice path. Measurements come from the dev box `64.227.170.31`
(pm2 `picoclaw-livekit`), device `00:16:3E:AC:B5:38`, with `--log-level debug`.

Branch carrying the fixes: `stt-ten-vad-only` (`58aebff`), built on `stt-sarvam-fixes`.
`main` is at `249999c` and still has every bug described below.

---

## What was wrong

### 1. The endpoint was batch-only

`wss://api.sarvam.ai/speech-to-text/ws` with `model=saaras:v3` completes a websocket
handshake with HTTP 101 and then never transcribes. Nothing errors and the socket
stays open, which is why this looked like a provider outage for two days — the
connection is healthy, it just has nothing to say.

Sarvam's own LiveKit guide confirms it: the legacy `sarvam.STT` class with
`model="saaras:v3"` supports batch only. The realtime service is a different URL and
a different model.

This also explains the single most confusing symptom: **the same audio transcribed
correctly over REST**. REST was hitting the batch API, which is what `saaras:v3` is.

| | wrong (main) | right |
| --- | --- | --- |
| URL | `wss://api.sarvam.ai/speech-to-text/ws` | `wss://api.sarvam.ai/speech-to-text-realtime/ws` |
| model | `saaras:v3` | `saaras:v3-realtime` |

### 2. Query parameters were silently ignored

The legacy parameter names are accepted without complaint by the realtime endpoint
and do nothing. `language-code` with a hyphen, `input_audio_codec=pcm_s16le`,
`flush_signal`, `vad_signals` and `high_vad_sensitivity` are not parameters of this
endpoint at all.

### 3. `language-code=unknown` is not a valid value

`normalizeSarvamLang` mapped `auto`, `unknown` and empty to the string `"unknown"`.
The realtime endpoint accepts `auto` or a concrete BCP-47 code and rejects
`unknown` — the first live session closed with code 4000, *Unsupported language_code
'unknown'*.

`auto` is accepted but deliberately not used. Measured over the same 18.49s of child
speech: with `language_code=auto`, 25 events and **zero** `transcript.final` in 30s;
with `en-IN`, the same 25 events and a final that arrives promptly. Since the pipeline
completes a turn on `IsFinal`, a session on `auto` streams partials forever and never
finishes a turn — worse than the bug it replaces, because it looks like it is working.

Sarvam's docs list concrete codes only and mention no auto fallback.

### 4. Sarvam's VAD closed every utterance after ~0.7s

The headline defect for children. Measured on dev:

| time | START→END | audio_duration | transcript | language_probability |
| --- | --- | --- | --- | --- |
| 16:55:04 | 0.64s | 1.248s | `"Rational"` (child said "National") | 0.179 |
| 16:56:21 | 0.70s | 1.312s | `"It's also called"` | 0.554 |

Children speak slowly with long mid-sentence pauses, so a 0.7s silence window chops
one sentence into ~1.3s fragments, each transcribed in isolation and each dispatched
as its own turn. Two of those turns had already paid for LLM tokens before the next
fragment cancelled them.

`silence_duration_ms` defaults to **500**. That is the knob; it was never being set.

### 5. The parser discarded replies without a word

Four paths in `parseMessage` returned `ok=false` with no log: unparseable JSON,
unknown signal type, unknown message type, and an empty transcript. "Sarvam sent
nothing" and "we threw away what Sarvam sent" were indistinguishable in the logs,
which sent two rounds of investigation after the VAD and then the stream close.

The empty-transcript case is the damaging one: Sarvam answers with nothing in it,
`RunInbound` keeps waiting for a transcript that has already been and gone, and the
turn hangs until something else ends it.

---

## What was tried and rejected

### `endpointing=manual` — TEN VAD owning the turn

The realtime endpoint supports `endpointing=manual`, where the server never decides
and the client sends `speech_start` / `speech_end` / `flush`. Deployed 2026-08-10
17:07 and reverted at 17:19.

It removed the 0.7s chopping and replaced it with nothing. Partials arrived; **no
`transcript.final` ever did**. The flush went out at 17:15:36 and the turn only ended
a second later on `vad_finalize_timeout`, dispatching the partial `"Dream."` — a
single word, the same fragmentation the change was meant to fix.

- `stt_first_partial_ms` = 17603
- `turn_total_e2e_ms` = 31715
- Sarvam also returned `transcriber_error` / `Backend: INTERNAL` during the session

Conclusion: the server's finalizer is what produces finals, and manual endpointing
switches it off. Sarvam's own guidance says the same thing from the other direction —
*"Pass `vad=None` and let Sarvam's server-side VAD drive turn-taking."*

### `vad_signals=false` on the legacy endpoint

Branch `stt-kid-vad-tuning` (`069b3ab`). Superseded — it was aimed at the legacy
endpoint, which does not transcribe at all.

### `stream_type=fast`

Considered for the 12s `stt_first_final_ms`, then rejected. `fast` buffers 500ms
against `balanced`'s 1000ms; less audio context per decode means worse accuracy.
Wrong lever when transcript quality is the priority.

---

## Current configuration

### Sarvam websocket (`pkg/voice/stt/sarvam_provider.go`)

```
wss://api.sarvam.ai/speech-to-text-realtime/ws
  ?language_code=en-IN          resolved per session; auto/unknown never sent
  &model=saaras:v3-realtime     realtimeSarvamModel() adds the suffix
  &mode=transcribe              SARVAM_STT_MODE
  &sample_rate=16000
  &encoding=linear16
  &stream_type=balanced         1000ms buffer — accuracy over latency
  &endpointing=vad              Sarvam's VAD owns the turn boundary
  &silence_duration_ms=1500     default is 500
Header: Api-Subscription-Key
```

Server defaults not overridden, echoed back in `session.begin`:
`threshold=0.3`, `prefix_padding_ms=300`, `min_speech_duration_ms=250`,
`return_timestamps=false`.

### Wire protocol

| direction | frame |
| --- | --- |
| client audio | `{"event":"audio_input","audio":"<base64 pcm>"}` as a **text** frame |
| client finalize | `{"event":"flush"}` |
| server | `session.begin`, `vad.speech_start`, `vad.speech_end`, `transcript.partial`, `transcript.final`, `error`, `pong`, `session.end` |

The key is `event`, not `type`, and transcript text is a top-level `text` field.
`vad.speech_end` carries neither text nor finality — claiming either ends a turn on
the first word or with nothing in it.

`speech_start` / `speech_end` are manual-mode events only. Do not send them under
`endpointing=vad`; they invite another `transcriber_error` for no benefit. `flush`
stays: harmless when the VAD has already finalised, and a safety net when it has not.

Numeric fields arrive **unquoted** even though the docs show them quoted
(`"confidence":"0.95"`). A string-typed struct field makes `Unmarshal` fail on the
whole message, so one type mismatch in a field nobody reads throws away the event
carrying it. `flexFloat` accepts either spelling.

### TEN VAD (`/root/.picoclaw/config.json` → `livekit_service.runtime`)

| key | was | now |
| --- | --- | --- |
| `vad_threshold` | 0.68 | **0.5** |
| `vad_endpoint_ms` | 1000 | **2000** |

Backup at `/root/.picoclaw/config.json.bak_vadtune`.

TEN VAD no longer owns the turn boundary — Sarvam's VAD does. It still drives
barge-in: a partial transcript arriving after VAD speech-start is what interrupts
agent audio.

Measured child speech probabilities: 0.686, 0.694, 0.699, 0.769, 0.779 — every one
sat *just* above the old 0.68 threshold. Note that `cea3c1d` measured this properly
with `kid_threshold_probe_test.go` and found child speech scores median 0.67-0.72
with p75 ~0.92, so ~50% of frames clear 0.68 and **the threshold was not the cause of
the silence**. 0.5 buys margin for quieter children; it does not fix anything by
itself.

### Env overrides do not work on `main`

`applyLiveKitRuntimeEnvOverrides` writes to `lkCfg`, a copy of `cfg.LiveKitService`,
and the manager-providers block below reassigns `lkCfg` from `cfg` — discarding every
`PICOCLAW_LIVEKIT_RUNTIME_*` override whenever a manager API is configured, which is
always. `PICOCLAW_LIVEKIT_RUNTIME_VAD_THRESHOLD=0.72` sat in the EKS manifest for
weeks while both pods ran config.json's 0.68.

Fixed in `cea3c1d`, which is **not** on `main`. Until that lands, **edit
`config.json` on the server, not the env**.

---

## Result after the fix

Session at 17:21, `silence_duration_ms=1500`:

| | before | after |
| --- | --- | --- |
| speech window | 0.64-0.70s | **8s** (17:21:38 → 17:21:46) |
| `stt_first_partial_ms` | 17603 | **3880** |
| `stt_first_final_ms` | — (timeout) | 12062 |
| `turn_total_e2e_ms` | 31715 | 23597 |
| transcript | `"Rational"`, `"Dream."` | full sentence |
| `missing_stt_final_marker` | true | **false** |

Partials built the whole utterance word by word, and a real `transcript.final` came
from Sarvam's VAD rather than our finalize timeout.

---

## Still open — Sarvam's side

### The final is worse than the partial before it

```
partial: "Now fact, it's also called Tiranga. It has three colors. Saffron,"
final:   "Nalpak. Na is also called Tiranga. It has three colors. Saffron."
```

The child said *"National flag, it's also called Tiranga…"*. Sarvam's own last partial
was closer to the truth than its final, and the final mangles the opening words into
something that is not a word at all. Consistent with the earlier `"National"` →
`"Rational"`. This is a model defect on child speech, not a setting.

Evidence to hand them: `request_id 20260810_7b45811a-d7c0-4723-9192-9a6eef04f7b2`
(from `session.begin`), plus the frame-by-frame partial log above.

### `transcriber_error: Backend: INTERNAL`

```json
{"event":"error","code":"transcriber_error","message":"Backend: INTERNAL","is_fatal":false}
```

Seen at 17:16:07. Non-fatal, server-side, no explanation.

### Latency

`stt_first_final_ms=12062` is still poor. `stream_type=fast` would halve the buffer at
a cost in accuracy — not worth it yet.

### Correlating a silent session

`request_id` only exists on sessions that reply. When Sarvam sends nothing there is no
id to report: the handshake response carries no `x-request-id`, no `cf-ray`, no trace
header of any kind — just `uvicorn` and the standard security set. Sessions that go
quiet can only be traced by source IP `64.227.170.31`, API key prefix, and the UTC
timestamp from Sarvam's own `Date` response header.

---

## Diagnostics added

Both live on `stt-ten-vad-only`:

- **Every inbound frame logged raw** (`Sarvam STT frame received`), not only the
  dropped ones. Drop logging alone left cleanly-parsed frames invisible, so a partial
  arriving 17s late looked identical to no partial at all. Also carries `request_id`.
- **Handshake response headers logged** on a successful dial. The dial previously kept
  `conn` and discarded `resp`, which is where a per-connection trace id would live if
  one existed.
- **Drop reasons logged** (`unparseable_json`, `unknown_signal_type`,
  `empty_partial`, `empty_transcript`, `unknown_message_type`) with a truncated
  payload.
- **Close caller named** — `closeCallerOutsideAdapter()` walks out of `sync` and this
  file, because `Close` runs inside `closeOnce.Do` and a fixed `runtime.Caller` depth
  lands in the stdlib.

## Branches

| branch | commit | state |
| --- | --- | --- |
| `main` | `249999c` | legacy endpoint, every bug above |
| `stt-sarvam-fixes` | `680d0de` | realtime endpoint, protocol fixes, `en-IN` |
| `stt-kid-vad-tuning` | `069b3ab` | superseded — legacy endpoint |
| `stt-ten-vad-only` | `58aebff` | **deployed on dev**, current config above |
