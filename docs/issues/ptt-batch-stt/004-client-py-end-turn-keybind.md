# 004 — client.py: end the turn locally with a keybind

**Type:** AFK · **Status:** open
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

- [ ] Pressing `s` while recording ends the recording and sends `speech_end`
- [ ] Gateway log shows the speech_end forward; a fresh turn can start afterwards
- [ ] `s` while not recording does nothing harmful
- [ ] Syntax check passes; manual smoke run against the dev gateway

## Blocked by

None — can start immediately.
