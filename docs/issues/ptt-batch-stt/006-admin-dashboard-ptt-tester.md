# 006 — Admin dashboard: push-to-talk tester with radial visualizer

**Type:** AFK · **Status:** closed · **Assignee:** claude
**Spec / Plan:** `docs/plan-admin-dashboard-ptt-tester.md` · ADR 0007
**Repo:** cheeko-backend `main/admin-dashboard` (branch `stt-ptt-batch`)

## What to build

A browser tester for Manual Talk in the existing admin dashboard's Test tab: a
radial audio visualizer (LiveKit agents-ui look), one push-to-talk button acting as
the device's knob, and a per-turn latency readout. Lets anyone exercise the
`sarvam_rest` PTT path from a laptop — no ESP32, no mic on the dev box, no client.py.

The Test tab already creates the room, dispatches the agent with gateway-identical
metadata, mints a browser token, and joins with `livekit-client`. This ticket adds
only front-end behavior on top of that session.

**Wire contract** — byte-identical to what the gateway forwards, published by the
browser on the LiveKit data channel, so the agent (issue 003) cannot tell the
difference:

| user action | published payload | mic |
|---|---|---|
| tap 1 (Talk) | `{"type":"ptt_event","action":"press","state":"start","mode":"manual","source":"admin_dashboard"}` | on |
| tap 2 (Done) | `{"type":"speech_end","source":"admin_dashboard"}` | off |
| Cancel (Esc) | `{"type":"ptt_event","action":"release","state":"stop","source":"admin_dashboard"}` | off |
| click while speaking | `{"type":"abort"}` then press | on |

**Visualizer states**, driven by local actions plus track energy (no dependency on
worker state messages): `connecting` → `idle` (mic off, waiting for a tap) →
`listening` (bars from the local mic analyser) → `thinking` (after speech_end, until
remote audio) → `speaking` (bars from the remote track analyser).

Hand-rolled `<canvas>`, not the React agents-ui kit — this app is deliberately
no-build vanilla JS.

Degrades safely: with a streaming provider active the agent ignores PTT messages
(issue 003's gate), so the button is just a mic toggle.

## Acceptance criteria

- [x] Tap → speak → tap publishes press then speech_end, and mic track enables then disables
- [x] Esc publishes release with `state:"stop"` and does not send speech_end
- [ ] Visualizer moves through connecting/idle/listening/thinking/speaking, bars respond to audio —
      **idle/listening/thinking verified; speaking and audio-reactive bars need a live agent**
- [x] Per-turn latency (speech_end → final transcript) is displayed
- [x] No changes to server.js, the gateway, the manager, or the agent
- [x] Page loads and the existing Test tab session flow still works

## Blocked by

- 003 (agent must act on the PTT data messages) — closed

## Resolution

Shipped in `bfaaef12` (cheeko-backend). `public/visualizer.js` (new, ~130 lines),
`public/test.js` (+170), `public/index.html` (+16), `public/styles.css` (+61).
Front-end only — server.js, gateway, manager and agent untouched, as the ticket
required.

The mic now starts **closed** and opens only between taps: in Manual Talk the
button is the turn boundary, so leaving the mic hot would have contradicted the
whole model.

**Verified in-browser** (dashboard on :4000, real page, real handlers driven
against a stubbed LiveKit room that captured every publish):

- tap 1 → `{"type":"ptt_event","action":"press","state":"start","mode":"manual"}`,
  mic `true`, button → "■ Done", state `listening`
- tap 2 → `{"type":"speech_end"}`, mic `false`, state `thinking`, latency armed
- Esc → `{"type":"ptt_event","action":"release","state":"stop"}`, mic `false`,
  state `idle`, and **no `speech_end`** — the distinction Cancel Turn depends on
- latency readout populated on the child's final transcript
- ring renders and animates (5632 painted pixels over 330 frames)
- no console errors from this code; the three present are an unreachable LiveKit
  server in this environment

**Unverified:** `speaking` state and audio-reactive bar heights need a live agent
track and a real microphone — neither exists here. Both are covered by 005's live
session. A room accidentally created during testing was torn down via `/lk/stop`
(`{"ok":true}`); nothing left running.

Not done, deliberately (per the plan): agents-ui React components, visualizer
config sliders, hold-to-talk, and the device-sim MQTT/UDP transport path.
