# GPT-Live response audio jitter — investigation and fix

Status: fixed on `feat/gptlive-elastic-playout` (commits `be80bacc`, `1dee308d`), verified smooth in a
local admin-dashboard session on 2026-09-15. Device (ESP32 → mqtt-gateway) test still pending.

## TL;DR

The Go GPT-Live agent (`pkg/gptlive` + `pkg/livekit/gptlive_pipeline.go`) produced choppy, stuttering
response audio. The Python worker (`cheeko-backend/main/python-agent`, livekit-agents
`GPTLiveModel`) on the same model and gateway did not.

There were two independent problems, and the second one was the one you could hear:

1. **Playout had no real buffer.** GPT-Live delivers audio at real time, so the fixed 100 ms priming
   lead drained and never refilled, and `PCMLocalTrack` zero-pads any partial 20 ms frame.
   Fixed with an *elastic* playout lead (200 ms, corrected only during model silence).
2. **The model was fed an irregular input clock.** GPT-Live generates output in step with its input
   audio. The Go agent pushed each mic frame to the socket the moment it arrived, and browsers send
   *nothing* during silence (Opus DTX). The model saw a stop-start clock and its output followed it:
   1.2–2.5 s of starved speech per reply. Fixed by doing exactly what the Python plugin does:
   24 kHz to the model, 100 ms chunks, pushed on a steady clock with silence filling the gaps.

## Background: how audio moves

```
browser / ESP32 mic
   │  WebRTC (Opus, DTX on) / MQTT+UDP via mqtt-gateway
   ▼
LiveKit room ──► agent: PCMRemoteTrack ──► WriteSample ──► gptlive.Session.PushAudio ──► OpenAI GPT-Live (websocket)
                                                                                                   │
LiveKit room ◄── agent: PCMLocalTrack ◄── driveSegmenter / driveElastic ◄── Session.Audio() ◄──────┘
   │
   ▼
browser speaker / mqtt-gateway (drops all-silent frames) ──► ESP32
```

Two facts about the pieces matter:

- **`lkmedia.PCMLocalTrack`** (server-sdk-go `pkg/media/pcmlocaltrack.go`) takes one 20 ms frame from
  its queue on a fixed ticker. If fewer samples are queued, it pads the rest of the frame with zeros.
  An empty queue means a silent frame; a partial one means a click mid-word.
- **GPT-Live is clocked by input audio.** With no input frames it neither speaks nor finishes a
  context injection. The probe hit this directly:
  `context_injection_incomplete: The session closed before the estimated context injection completed`.

## Symptoms

- Response audio stuttered and dropped syllables in the Go agent, on the device and in the browser.
- The Python worker on the same OpenAI account, model (`gpt-live-1`) and gateway was smooth.
- Earlier fixes on `feat/gpt-live-go` helped but did not cure it:
  - `9fa2c36e` added a 500 ms playout lead;
  - `d352130b` found the read goroutine blocking on a slow consumer (TCP backpressure made OpenAI look
    slow), decoupled it, and cut the lead to 100 ms.

## Investigation

### 1. Playout, in isolation (probe)

`cmd/gptlive-playout-probe` joins a room and talks through the real `pkg/gptlive` session with no
tools, persona or persistence. It runs either the old playout (`-playout old`, a port of
`driveSegmenter`) or the new one (`-playout elastic`) and logs every 5 s: queued level, underruns,
arrival ratio, ticker gaps. A headless `lk room join --auto-subscribe` listener is enough; the probe
feeds steady silence as its input so the model has a clock.

Local LiveKit, 16 kHz, ~80 s of continuous speech each:

| | underruns | starved speech | lead after a dip |
|---|---|---|---|
| old (100 ms priming) | 9 | ~117 ms | fell to 15–30 ms and stayed there |
| elastic (200 ms target) | 1 | 41 ms | refilled to 200–290 ms in silence |

Arrival ratio was 0.96–1.02: OpenAI delivers at real time. A buffer only refills if the source runs
faster than playout, so any fixed lead is eventually spent by network dips and never comes back.

### 2. The full agent, with elastic playout

Run from the admin dashboard GPT-Live tab. Elastic playout was working as designed, but:

```
gptlive: elastic playout burst  underrun_ms=1259  level_ms=0
gptlive: elastic playout burst  underrun_ms=1805  level_ms=165
gptlive: elastic playout burst  underrun_ms=2541  level_ms=0
```

Same model and code as the smooth probe, far worse output. The difference was the input: the probe
fed perfectly steady silence; the agent forwarded the browser mic.

### 3. Comparing with the Python plugin

Read from the dev box venv: `livekit/plugins/openai/realtime/gpt_live_model.py` (livekit-agents 1.8.x).

| | Python plugin | Go agent (before) |
|---|---|---|
| Rate to GPT-Live | always 24 kHz (`SAMPLE_RATE = 24000`), resampled with `rtc.AudioResampler` | the session rate, 16 kHz for device-shaped metadata |
| Mic → model | `AudioByteStream(samples_per_channel=SAMPLE_RATE // 10)`: fixed 100 ms chunks, ~10 msgs/s | every `PCMRemoteTrack` frame pushed immediately, ~50–100 msgs/s, each able to block up to 1 s on a full send queue |
| Mic timing | frames come from the native LiveKit audio stream at a steady cadence | frames arrive as packets arrive; nothing during DTX silence |
| Output | every frame, silence included, into `rtc.AudioSource(queue_size_ms=200)` | `PCMLocalTrack` fed with a 100 ms priming lead |

### 4. Measuring both ends

Added a 5 s `gptlive: audio flow` log in the agent and WebRTC stats in the dashboard. After the fix:

```
in_ratio=0.24  in_max_gap_ms=404  push_max_ms=0  out_ratio=0.98  out_max_gap_ms=169  underrun_ms=0  level_ms=218
in_ratio=0.14  in_max_gap_ms=402  push_max_ms=0  out_ratio=1     out_max_gap_ms=111  underrun_ms=0  level_ms=198
```

`in_ratio` of 0.1–0.8 with ~400 ms gaps *while the user is talking* is Opus DTX: the browser sends no
packets during silence. That is the irregular clock the model was following before the fix.

## Root causes

1. **Fixed playout lead against a real-time source.** A 100 ms lead is spent by the first dip over
   100 ms and never refills; the old re-arm logic then held back another 100 ms, turning a dip into a
   guaranteed hole. Writes were not frame-aligned, so the track zero-padded partial frames (clicks).
2. **Irregular input clock.** Per-frame, as-they-arrive mic pushes with DTX gaps gave GPT-Live a
   stop-start input, and its generated audio came back with the same shape.

## The fix

### Elastic playout — `pkg/livekit/gptlive_pipeline.go` `driveElastic` (`be80bacc`)

- Track a `level` that mirrors `PCMLocalTrack`'s queue: plus every write, minus wall time.
- Hold a 200 ms lead (`elasticPlayoutTarget`, the same as livekit-agents' room output queue).
- Correct it **only while the noise gate says the model is silent**: pad silence when `level` is under
  the target, drop silence when it is over twice the target. Speech is never held or dropped.
- Write whole 20 ms frames only, carrying the remainder, so the track never zero-pads mid-speech.
- Enabled with `PICOCLAW_GPTLIVE_PLAYOUT=elastic`; the old `driveSegmenter` path is the default until
  the device test passes.

### Python-style input — `pumpMicIn` + `gptlive_spec.go` (`1dee308d`)

- `buildGPTLiveSpec` always uses **24 kHz**. `PCMRemoteTrack` resamples the room mic to it and Opus
  carries the output, so clients never see the rate.
- `WriteSample` only appends to a mic buffer (capped at 500 ms, oldest dropped, so a stall cannot grow
  input latency).
- `pumpMicIn` sends exactly **100 ms every 100 ms**. It waits for 200 ms of buffered mic before taking
  mic audio, sends silence until then and whenever the mic runs dry, then rebuilds that slack. The cost
  is ~100 ms of added input latency.
- The 5 s `gptlive: audio flow` log stays in for now (fields below).

### Result

Local admin-dashboard session, ~90 s: **0 ms underrun in every window**, playout lead steady at
200–360 ms, model audio at 0.92–1.00× real time, send queue never blocking (`push_max_ms` 0–9 ms,
one 169 ms spike). Audibly smooth.

### Related changes in cheeko-backend (`9f290e21`, branch `feat/python-agent-picoclaw-parity`)

- `manager-api-node` `getActiveProviders`: a missing `realtime_providers` table (Prisma `P2021`) now
  reports `realtime: null` instead of a 500 that took LLM/STT/TTS config down for every agent.
- `admin-dashboard` GPT-Live tab: WebRTC audio stats in the session log every 5 s.

## Reading the diagnostics

### Agent — `gptlive: audio flow` (elastic playout only)

| field | meaning | healthy |
|---|---|---|
| `in_ratio` | mic audio received ÷ wall time | anything; low values are DTX, the pump fills them |
| `in_max_gap_ms` | longest gap between mic frames | anything, same reason |
| `push_max_ms` | slowest `PushAudio` call | near 0; high means the websocket send queue is full |
| `out_ratio` | model audio received ÷ wall time | ~1.0 while the model is streaming |
| `out_max_gap_ms` | longest gap between model audio chunks | below the playout lead (200 ms) |
| `underruns` / `underrun_ms` | separate starvation gaps during speech, total starved time | 0 |
| `level_ms` | audio queued in the local track | around 200 ms |

### Dashboard — session log

`audio in: N pkts, lost L, jitter J ms, buffer B ms, concealed C ms (G gaps) · mic out: M pkts`

- ~250 packets per 5 s is a full 20 ms stream.
- **Concealed** is audio the browser invented because packets were late, i.e. what you hear as jitter.
  The line turns red above 40 ms.
- Agent log clean but concealment high means the problem is between the agent and the browser.

## How to reproduce and test locally

Build (Windows, cgo via MSYS2):

```powershell
$env:Path = "C:\msys64\mingw64\bin;$env:Path"; $env:CC = "gcc"; $env:CGO_ENABLED = "1"
go build -o bin\picoclaw-livekit-gptlive.exe .\cmd\picoclaw-livekit
go build -o bin\gptlive-playout-probe.exe .\cmd\gptlive-playout-probe
```

Run the agent against local LiveKit (keys from your local LiveKit config; OpenAI key from the manager's
Realtime provider row or the dev box `.env`):

```powershell
$env:OPENAI_API_KEY = "<gpt-live key>"
$env:PICOCLAW_LIVEKIT_PIPELINE = "gptlive"
$env:PICOCLAW_GPTLIVE_PLAYOUT = "elastic"     # "old" for the before comparison
.\bin\picoclaw-livekit-gptlive.exe -agent-name cheeko-gptlive-go -config .\config.json -log-level info
```

Then admin dashboard → GPT-Live tab → Agent `cheeko-gptlive-go` → ask for a long story.

Probe A/B without the dashboard:

```powershell
$env:LIVEKIT_URL = "ws://127.0.0.1:7880"; $env:LIVEKIT_API_KEY = "<key>"; $env:LIVEKIT_API_SECRET = "<secret>"
.\bin\gptlive-playout-probe.exe -room probe-1 -playout old -duration 90s
.\bin\gptlive-playout-probe.exe -room probe-1 -playout elastic -duration 90s
```

Join with the printed `listen:` link, or headless:
`lk room join --url ws://127.0.0.1:7880 --api-key <key> --api-secret <secret> --identity listener --auto-subscribe probe-1`.

## Local environment gotchas found along the way

- **Two LiveKit servers on `localhost:7880`.** Docker Desktop listens on IPv4 `127.0.0.1`; a WSL LiveKit
  is relayed on IPv6 `::1`. Go resolves `localhost` to IPv4, Node and Chrome to `::1`, so the agent and
  dashboard silently used different servers. Use `ws://127.0.0.1:7880` everywhere (picoclaw
  `config.json`, `mqtt-gateway/config/mqtt.json`, `admin-dashboard/.env`).
- **`admin-dashboard/.env` wins over the gateway config.** It pointed at LiveKit Cloud; the room and
  dispatch went there while the agent waited locally.
- **`livekit_service.server_url` in `config.json` wins over `PICOCLAW_LIVEKIT_SERVER_URL`.**
- **Headless probe listeners need an input clock.** Without mic frames GPT-Live never speaks; the
  probe feeds silence.
- **Local test DB lacked migration `20260913000000_realtime_providers_gptlive_voice`.** It broke
  `/livekit/providers/active` and the dashboard template list (`ai_agent_template.gptlive_voice`).
  Applied with `prisma db execute --file` only, never `migrate deploy` (the shared test DB has
  desynced migrations).

## Remaining work

1. Test through the real ESP32 → mqtt-gateway path on the dev box. The gateway drops all-zero frames;
   check whether elastic's silence padding interacts badly with device playback.
2. Once that passes, make elastic playout the default and delete `driveSegmenter`'s lead buffer,
   `audioLeadBuffer`/`audioLeadQueueCap` and the `PICOCLAW_GPTLIVE_PLAYOUT` switch.
3. Decide whether the `gptlive: audio flow` log stays (info) or drops to debug.
4. Consider a unit test for `pumpMicIn`; it currently talks to the concrete `*gptlive.Session`, so it
   needs a small push seam first.
