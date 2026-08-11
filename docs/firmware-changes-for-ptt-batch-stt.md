# Firmware changes for PTT + batch STT (cheeko-os-v2)

> Written 2026-08-11 after auditing `cheeko-os-v2` against the server behaviour
> shipped on `stt-ptt-batch`. Context: ADR 0007 (picoclaw).

## Summary

**One required change.** The tap-talk-tap protocol the server now depends on is
already what the firmware sends — that was the point of the design, and it is why
this is a one-item list rather than a rewrite.

| # | change | priority |
|---|---|---|
| 1 | Cap the manual-turn recording duration and auto-end the turn | **required** |
| 2 | Translate the empty-tap phrase (server-side, listed for awareness) | optional |
| 3 | Keep auto-listening / wake-word off on `sarvam_rest` devices | config, not code |

Everything else — `listen/start` on tap-1, `speech_end` on tap-2, `listen/stop`
on cancel, the abort+restart on tap-3 — already matches what the agent expects,
byte for byte.

---

## 1. Cap the manual-turn duration (required)

### The problem

In Manual Talk the firmware places **no upper bound** on how long the mic stays
open. On entering Listening:

```cpp
// application.cc:3052
listening_ticks_ = listening_mode_ == kListeningModeManualStop ? 0 : 1;
```

`0` disables the 30-second no-voice watchdog (guarded by `if (listening_ticks_ > 0)`
at `:939`). And while audio is flowing, `MAIN_EVENT_SEND_AUDIO` resets the counter
on every tick:

```cpp
// application.cc:826
if (!awaiting_server_reply_) {
    listening_ticks_ = 1;  // reset listening timeout while sending audio
}
```

So a child who taps and keeps talking holds the mic open indefinitely. That was
harmless when server-side VAD ended turns. It is not harmless now.

### Why it matters now

The server side has hard limits the firmware knows nothing about:

| limit | value | where |
|---|---|---|
| Sarvam REST clip limit | **30s** — returns HTTP 400 | verified in issue 001 |
| Provider buffer cap | 30s, extra audio dropped with a warning | `sarvam_rest_provider.go` |
| Segment hard cap | **25s** — force-finalizes the turn | `audio_pipeline.go` |

A 60-second ramble therefore plays out like this: at 25s the server finalizes and
answers the *first* 25 seconds while the child is still talking; the rest of their
speech goes into a buffer that is capped and later discarded; the child's eventual
tap-2 sends `speech_end` on a buffer holding only the tail. From the child's side
Cheeko interrupts, then ignores the rest of what they said.

### The change

Re-arm the listening watchdog in manual mode with a shorter, purpose-built cap —
around **20 seconds**, comfortably under the server's 25s — and when it fires,
end the turn exactly as tap-2 does rather than killing the session:

- call the same path as `SendSpeechEnd()` (`application.cc:3494`), so the mic
  closes, `awaiting_server_reply_` is set, and the thinking cue starts
- do **not** reuse the existing 30s listening timeout: that one ends the whole
  session (`CloseAudioChannel`, `TrackAiTalkEnd("listening_timeout")`), which is
  the wrong outcome for a child who simply talked a long time
- optionally give a cue at the cap (a short chime, or the display switching to
  "Thinking…" on its own) so the turn ending feels intentional rather than broken

A separate counter is cleaner than reusing `listening_ticks_`, since that one is
deliberately reset by `MAIN_EVENT_SEND_AUDIO` on every audio tick.

### How to verify

Tap, talk continuously past the cap, and confirm: the mic closes at ~20s, one
`speech_end` goes out, a reply arrives covering what was actually said, and the
session continues normally into the next turn.

---

## 2. Empty-tap phrase language (awareness only)

When a tap produces no intelligible speech, the agent now speaks
*"I didn't hear you! Press the button and try again."* — English only in v1.

Nothing for the firmware to do; it plays this like any other TTS. Listed so it
isn't mistaken for a firmware string. The fix belongs in the per-language phrase
switch in picoclaw's `audio_pipeline.go`, alongside the existing greeting and
retry phrases.

---

## 3. Auto-listening must stay off (configuration)

`sarvam_rest` derives the turn boundary **only** from the knob. A device running
auto-listening or wake-word mode against a worker with `sarvam_rest` active has
no turn boundary at all, and every turn would run to the 25s segment cap.

This is a fleet/config constraint rather than a firmware code change, but it is
the firmware setting that decides it: `auto_listening_enabled_` must be false, so
`GetDefaultListeningMode()` (`application.cc:3151`) returns
`kListeningModeManualStop`.

If mixed modes are ever needed, the fix belongs on the server — the
`ptt_event.mode` field already carries `manual` / `auto` / `realtime`, so the
agent can fall back to VAD for non-manual sessions.

---

## What deliberately needs no change

Audited and already correct:

| firmware behaviour | server expectation | match |
|---|---|---|
| tap-1 → `listen/start` with `mode=manual` (`:3061`) | `ptt_event action=press` → reset buffer, open turn | ✓ |
| tap-2 → `speech_end` (`:3494`), mic off, `awaiting_server_reply_` | finalize → REST transcription | ✓ |
| double-click → `listen/stop` (`:2879`) | `action=release state=stop` → discard, stay silent | ✓ |
| tap-3 → `SendAbortSpeaking` + fresh `listen/start` (`:2535`) | abort then a clean new turn | ✓ |
| 40s thinking watchdog | replies now land in ~1s instead of 12s | ✓ far inside |
| TTS accepted while `awaiting_server_reply_` (`:1800`) | the empty-tap announcement arrives this way | ✓ |

The faster replies are worth noting on their own: the streaming path measured
`stt_first_final_ms=12062`, and the REST smoke test came back at ~600ms for an
18-second clip. Nothing in the firmware assumed the slow path, so this needs no
adjustment — it just stops living near the watchdog.
