# Plan — Push-to-Talk tester in admin-dashboard (visualizer UI)

> Repo: cheeko-backend `main/admin-dashboard` (branch `stt-ptt-batch`).
> Status: PLAN — no code until approved.
> Companions: ADR 0007, `docs/plan-stt-ptt-batch.md`, issues `ptt-batch-stt/001-005`.

## Goal

A browser page in the existing admin dashboard that talks Manual Talk (tap-talk-tap)
to the agent and looks like LiveKit's agents-ui visualizer: a radial audio visualizer
driven by agent state (connecting / listening / thinking / speaking), a single PTT
button as the "knob", live transcript + latency readout. Lets anyone test the
`sarvam_rest` PTT path from a laptop — no ESP32, no mic-equipped dev box, no client.py.

## Architecture decision: extend the existing Test tab (LiveKit-direct)

The dashboard already has everything hard:

- `POST /session` (server.js:135) — creates the room, attaches the gateway-identical
  dispatch metadata, dispatches the worker, mints a browser join token.
- The browser already joins with `livekit-client` and publishes its mic.
- Issue 003's agent code reads PTT signals **from the LiveKit data channel** in the
  gateway's wire shape — a browser participant can publish those exact payloads
  itself. The gateway is not needed for this test (its forwarding is already proven
  via client.py in 004/005).

So: no gateway changes, no server changes, no new transport. The PTT tester is a
front-end feature of the existing Test tab.

**UI kit decision:** the screenshot is LiveKit's agents-ui React/shadcn components,
but this repo is deliberately no-build vanilla JS. Hand-roll the radial visualizer in
a `<canvas>` (~70 lines: N bars on a circle, lengths from a WebAudio AnalyserNode,
colors per state). Same look, zero build system. If pixel-parity with agents-ui ever
matters, that's a separate React micro-app — out of scope.

## Wire contract (what the browser publishes)

Byte-identical to what the gateway forwards, so the agent (room_session.go cases from
issue 003) can't tell the difference:

| user action | data-channel publish | mic track |
|---|---|---|
| tap 1 — Talk | `{"type":"ptt_event","action":"press","state":"start","mode":"manual","source":"admin_dashboard"}` | enable |
| tap 2 — Done | `{"type":"speech_end","source":"admin_dashboard"}` | disable |
| Cancel (Esc / ✕) | `{"type":"ptt_event","action":"release","state":"stop","source":"admin_dashboard"}` | disable |
| interrupt while speaking | `{"type":"abort"}` then press | enable |

Keyboard: Space = tap (toggle), Esc = cancel — mirrors client.py's `s`/spacebar.

Works safely with any active STT provider: the agent ignores PTT messages unless
`sarvam_rest` is active (issue 003 gate), so with a streaming provider the button
degrades to a plain mic on/off toggle.

## Visualizer state machine (client-derived, provider-agnostic)

```
connecting  room joining → agent audio track subscribed (or greeting heard)
idle        mic off, waiting for a tap (dim, slow breathe)
listening   between press and speech_end — bars driven by LOCAL mic analyser (green)
thinking    speech_end sent → first remote audio frame (amber spinner pulse)
speaking    remote track audio playing — bars driven by REMOTE analyser (cyan)
```

Primary source is local actions + remote-track energy (always available). The
worker's `agent_state_changed` data messages are consumed opportunistically as a
correction signal when present, not required.

## Extras that earn their place

- **Transcript line:** the worker publishes transcription/`speech_created` data
  messages the gateway normally consumes — show final transcript text when it lands.
- **Latency readout:** `t(speech_end click) → t(final transcript)` in ms, on screen.
  This is the number the whole sarvam_rest project chases (12s → target <1.5s);
  seeing it per turn beats grepping pm2 logs.
- **Turn log:** last N turns with state timings, so empty-tap ("didn't hear") and
  cancel behavior are visible in one glance.

## Changes (all in `main/admin-dashboard/`, all front-end)

| # | file | what | size |
|---|---|---|---|
| 1 | `public/visualizer.js` (new) | canvas radial visualizer: bar ring, per-state colors/animations, `attachAnalyser(node)`, `setState(s)` | ~70 lines |
| 2 | `public/test.js` | PTT state machine: publishes from the table above, mic enable/disable, WebAudio analysers (local + remote), state transitions, transcript/latency capture | ~90 lines |
| 3 | `public/index.html` + `styles.css` | canvas + round PTT button + state label + transcript/latency line in the Test tab | ~50 lines |

Server, gateway, manager, agent: **zero changes.**

## Not doing (deliberate)

- React/agents-ui components — conflicts with the repo's no-build idiom.
- Config sliders from the screenshot (hue/radius/bar count) — constants in
  visualizer.js; a tester doesn't need theme knobs.
- device-sim transport toggle (browser mic → MQTT/UDP full-chain) — the full chain
  is client.py + 005's job. Add later only if the dashboard must also regression-test
  the gateway. device-sim.js already has `speech_end`; it would need only a
  `pressTalk()` method (~10 lines) that day.
- Hold-to-talk mode — firmware is tap-toggle, the tester should match the product.

## Verify (manual, against dev box services)

1. Manager flip → `sarvam_rest`, dashboard `.env` pointed at dev manager + LiveKit.
2. Happy turn: tap → speak → tap → transcript + spoken reply; latency shown <2s.
3. Empty tap: tap → tap (silence) → "I didn't hear you!" plays, state returns to idle.
4. Cancel: tap → speak → Esc → silence, no reply, next turn works.
5. Barge-in: click during speaking → reply stops, mic opens.
6. Flip manager back to `sarvam` → button still toggles mic, VAD drives turns
   (degraded-not-broken confirmed).

## Estimate

~210 lines across 4 files, no dependencies added, one afternoon including manual
verification. Slots in as issue `ptt-batch-stt/006` (blocked by 003, parallel to 005).
