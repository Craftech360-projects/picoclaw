# 004 — client.py: end the turn locally with a keybind

**Type:** AFK · **Status:** closed · **Assignee:** claude
**Spec / Plan:** `docs/plan-stt-ptt-batch.md` (C2)
**Repo:** cheeko-backend (branch `stt-ptt-batch`)

## What to build

The simulator already sends the Manual Talk boundary messages (`listen/start` when its
mic opens, `speech_end` when recording ends — done earlier on this branch), but
recording today only ends when the **server** sends `record_stop` — circular for
testing a device-driven Turn Boundary.

Add a key (`s`) to the existing keyboard-monitor thread that stops the current
recording locally, exactly as tap-2 does on the device: recording loop exits → mic
closes → `speech_end` goes out. Spacebar (abort) and number keys (RFID) keep their
current meanings.

## Acceptance criteria

- [x] Pressing `s` while recording ends the recording and sends `speech_end` — verified by code trace, not a live run (see Resolution)
- [ ] Gateway log shows the speech_end forward; a fresh turn can start afterwards — **unverified, needs the user**
- [x] `s` while not recording does nothing harmful — verified by code trace
- [x] Syntax check passes — `ast.parse` clean. Manual smoke run — **not done, needs the user** (see Resolution)

## Blocked by

None — can start immediately.

## Resolution

Shipped in `efbea573` (cheeko-backend). Two pieces landed together since both were
uncommitted PTT work sitting in the working tree: `_send_ptt()` (the `listen/start` /
`speech_end` boundary messages, approved earlier this session, never actually
committed) and the `'s'` keybind itself (~15 lines in the existing
`monitor_spacebar` thread, same press/debounce/release pattern as the spacebar and
RFID-digit handlers already there).

Mechanism: `'s'` calls `stop_recording_event.set()` — the exact same event tap-2
sets on the device — which breaks the recording thread's inner loop and falls
through to the already-wired `self._send_ptt("end")` call. No new state machine,
reused the existing one.

**Why two criteria are unticked, not faked:** this environment has no microphone,
no interactive global-hotkey session (the `keyboard` library needs one), and no
confirmed live gateway target — `SERVER_IP`/`MQTT_BROKER_HOST` in the working tree
point at a local network address that predates this session and isn't mine to
assume is live. Verified everything reachable by code trace instead: the inner-loop
exit path, and the clear-before-arm invariant in the TTS-stop handler that makes an
idle `'s'` press safe. The live run — press `s`, watch the gateway log for
`[SPEECH-END]`, confirm the next turn starts clean — needs your hands on the
keyboard against the dev gateway.

**One-round review** (single focused Sonnet reviewer, proportionate to a 15-line
diff) found one real issue: a microsecond race between the new handler and the
pre-existing (unchanged) non-atomic `clear()`/`set()` pair in the TTS-stop handler
could silently skip one turn's audio. Window is smaller than human key-press timing
and the file has no locking precedent to extend — documented inline with a
`ponytail:` comment (re-press if a turn ever looks empty) rather than adding new
synchronization primitives to a manual test script.

**Not committed:** an unrelated, pre-existing `SERVER_IP`/`MQTT_BROKER_HOST` change
in the working tree — not part of this ticket, left as a local uncommitted edit via
a hand-built partial patch rather than swept in by a blanket `git add`.

Does not unblock 005 by itself — 005 also needs 003 (agent wiring, not started).
