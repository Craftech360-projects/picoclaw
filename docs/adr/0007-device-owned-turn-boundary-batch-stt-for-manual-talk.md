# 7. Device-owned Turn Boundary and batch STT for Manual Talk

Date: 2026-08-11

## Status

Accepted

## Context

Child speech broke streaming STT turn detection. Sarvam's server VAD needed a
1500ms silence window (their guidance: 800ms) because children pause mid-sentence;
even then `stt_first_final_ms` measured 12062ms, and streaming finals sometimes came
back worse than the partials preceding them (`docs/sarvam-stt-vad-findings.md`,
request_id `20260810_7b45811a`). TEN-VAD-owns-the-turn and widened-silence variants
were built and measured on the dev box before this decision; the findings doc records
why each was insufficient.

Meanwhile the product's Manual Talk mode (tap-talk-tap on the Talk card) already
produces an exact, human-authored turn boundary: the firmware sends `speech_end` on
tap-2 and turns its mic off. That signal reached the agent over the LiveKit data
channel and was discarded.

Sarvam's LiveKit guidance says to use their VAD and disable local VAD. Their
sync REST endpoint (`saaras` family, <30s clips) produced visibly better transcripts
on the same child audio than the streaming websocket.

## Decision

In Manual Talk sessions, the **device owns the Turn Boundary** and STT is **one
Sarvam REST call per utterance** (`sarvam_rest` provider: buffer PCM between
`ptt_event` press and `speech_end`, then POST `saaras:v4`, `language_code=unknown`).
No VAD — TEN or Sarvam — participates in the Manual Talk path. `speech_end` means
End Turn (process); `listen/stop` means Cancel Turn (discard, stay silent).

## What this does NOT decide

Streaming STT + VAD remains the path for auto-listening and wake-word modes and
stays intact in the codebase. Which path a worker runs is the manager's active
STT provider flip (`sarvam` ↔ `sarvam_rest`); per-session mixing of the two
authorities inside one session is deliberately not allowed.

## Consequences

- No VAD tuning for Manual Talk, ever. Boundary bugs become protocol bugs, which
  are debuggable from logs.
- Batch-model transcript quality; the streaming final-worse-than-partial failure
  mode disappears.
- Expected first-final ~1–2s after tap-2 (REST on complete clip) vs 12s streaming.
- No streaming partials: no live transcript during speech, and barge-in while the
  child speaks is gone — irrelevant in Manual Talk (mic is off during TTS; interrupt
  is knob-driven `abort`).
- Utterances are capped (~25s segment cap, 30s REST limit); acceptable for
  knob-bounded child turns.
- Firmware tap-talk-tap contract (press / speech_end / stop) becomes load-bearing
  across three repos; reversing after firmware ships is costly.
