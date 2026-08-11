# PTT + batch STT — what still needs doing

> Written 2026-08-11 at the end of the build session. Branch `stt-ptt-batch` in
> **picoclaw** and **cheeko-backend**, deployed to the dev box, nothing merged to `main`.
> Background: ADR 0007, `docs/plan-stt-ptt-batch.md`, issues `ptt-batch-stt/001-006`.

## Where things actually stand

| piece | state |
|---|---|
| `sarvam_rest` STT provider (buffer → WAV → REST) | shipped, 10 unit tests |
| Agent PTT wiring (press / speech_end / cancel, no VAD) | shipped, 18 tests |
| "I didn't hear you" empty-tap announcement | shipped |
| client.py tap-to-talk (`t` opens mic, `t` again ends turn) | shipped, works |
| Admin dashboard PTT tester + radial visualizer | shipped |
| DB: `sarvam_rest` active (local + dev box) | done, `sarvam` kept for rollback |
| Dev box worker built and running the new binary | done |
| **A real end-to-end voice turn through the new path** | **never run** |

Everything is proven in unit tests and at the protocol level. Nothing has yet
carried a child's voice from a device through Sarvam REST to a spoken reply.

---

## 1. Must happen before this is real

### 1.1 Run the live end-to-end test (issue 005)
The single most important gap. Point a device or client.py at the dev box and
confirm, in the worker log:

```
Resolved per-session provider selection … stt_provider=sarvam_rest stt_model=saaras:v4
Sarvam REST transcript received … elapsed_ms=…
```

Scenarios to cover: a normal turn; a silent tap (should say "I didn't hear you");
a cancel (should stay silent); several turns back to back (no audio bleed);
then flip the DB back to `sarvam` and confirm streaming still works.

Record the `elapsed_ms` numbers — the streaming baseline was 12062ms and the
smoke test suggested ~600ms, so this is the payoff measurement.

### 1.2 Decide what happens to non-manual sessions
**This is the sharpest edge in the whole design.** The STT provider is chosen
per *worker*, from one global DB row — but PTT boundaries only exist in Manual
Talk. If any device runs auto-listening or wake-word mode against a worker with
`sarvam_rest` active, that session has **no turn boundary at all** and will only
finalize on the 25s segment cap.

Options, cheapest first:
- Confirm the fleet is Manual-Talk-only and write that down as a constraint.
- Have the agent fall back to VAD when a session's mode isn't manual (the
  `ptt_event.mode` field already carries it — `application.cc` sends
  `mode=manual` / `auto` / `realtime`).
- Make provider selection per-session rather than per-worker.

Do not roll this beyond the dev box until this is answered.

### 1.3 Verify the firmware needs no changes
The design deliberately reuses signals the firmware **already sends**
(`listen/start`, `speech_end`, `listen/stop`), so the expectation is **zero
firmware changes**. That expectation is untested on hardware. Confirm on a real
device that:
- tap-1 → `listen/start`, tap-2 → `speech_end` reach the gateway
- the reply arrives well inside the firmware's 40s thinking watchdog
- double-click cancel produces silence, not a late answer
- the empty-tap announcement unlocks the device normally

If all four hold, the firmware phase is closed without a single line of C++.

---

## 2. Should happen soon

### 2.1 Merge decision
Nothing is merged. Both repos have `stt-ptt-batch` sitting on top of `main`
(`main` has not moved). Also still unmerged from the earlier debugging:
`stt-sarvam-fixes`, `stt-ten-vad-only`, `feat/sarvam-rest-stt`.

Note: `feat/riddle-bank` in cheeko-backend has the **same tree hash as `main`** —
its content already landed. Merging it would be a no-op; it can simply be deleted.

### 2.2 Undo the local LiveKit pin
I set the local Docker LiveKit to `node_ip: 127.0.0.1` to work around a stale
`192.168.0.193`. That makes it unreachable from phones and other machines.
There is already a proper fix on `feat/riddle-bank`:

```
4b59217e fix(livekit-local): advertise the host LAN ip as the ICE candidate
```

Adopt that approach instead of the localhost pin.

### 2.3 Housekeeping
- A second admin dashboard is running on **:4001** (started for testing with a
  localhost LiveKit override). Kill it; the real one is on :4000.
- The dev box's `admin-dashboard` (pm2 id 25) has 11 days uptime, so it still
  serves the pre-switch files. `pm2 restart admin-dashboard` to pick up the PTT tester.
- Check Sarvam's REST vs websocket **pricing**. The cost model assumed streaming;
  per-request billing may differ, and `docs/cheeko-costing-sheet.xlsx` should be
  updated once a real per-turn cost is known.

---

## 3. Known gaps, deliberately left

These are documented in code with `ponytail:` comments and in the issue
resolutions. None block the dev-box test.

| gap | consequence | real fix |
|---|---|---|
| Empty-result callback carries no utterance identity | Two turns finalized inside one REST round-trip (press/end/press/end in ~1s) can announce "I didn't hear you" for the wrong turn | Per-utterance id threaded through the provider callback — would also fix the next row |
| Overlapping `Finalize` calls emit in HTTP-completion order | 25s hard cap racing a fast `speech_end` can dispatch two turns out of speech order | Same as above; the two share one root cause and are worth doing together |
| TEN-VAD-absent-for-`sarvam_rest` proven only for the predicate | A future refactor could invert the construction branch with tests still green | Needs a `handleTrackSubscribed` harness with real webrtc objects |
| client.py microsecond race on `stop_recording_event` | A tap landing in a tiny window can skip one turn's audio silently | A lock in a manual test script; not worth it unless it bites |
| Visualizer `speaking` state + audio-reactive bars | Unverified — needs a live agent track and a real mic | Covered by 1.1 |
| "I didn't hear you!" is English only | A Hindi-speaking child hears English | Add to the existing per-language phrase switch in `audio_pipeline.go` |

---

## 4. Still open from before this work

- **The Sarvam support message** is drafted but never sent. Two genuine issues
  remain theirs: a `transcript.final` worse than its preceding partial
  (`request_id 20260810_7b45811a-…`), and an unexplained
  `{"code":"transcriber_error","message":"Backend: INTERNAL"}`. Both are about
  the **streaming** path, which this work routes around but does not fix — still
  worth reporting if streaming is ever the fallback.
- **Production rollout** (EKS, `picoclaw-dev` namespace) is untouched and should
  stay that way until §1.2 is answered and §1.1 has passed.

---

## Rollback

One statement, no redeploy, picked up on the next TTL tick:

```bash
psql "$DATABASE_URL" -c "UPDATE stt_providers SET is_active=(provider_name='sarvam')"
```

The streaming path was never removed — `sarvam` still works exactly as before.
