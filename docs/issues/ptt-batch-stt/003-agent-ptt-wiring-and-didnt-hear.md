# 003 — Agent wiring: PTT signals drive the turn, no VAD, didn't-hear announcement

**Type:** AFK · **Status:** open
**Spec / Plan:** `docs/plan-stt-ptt-batch.md` (P2) · ADR 0007
**Repo:** picoclaw (branch `stt-ptt-batch`)

## What to build

Make the device's Manual Talk signals the session's Turn Boundary authority when the
`sarvam_rest` provider is active, by translating data-channel messages into the same
synthetic speech-start/speech-end events the VAD path already feeds the pipeline —
the inbound turn loop itself stays untouched.

Semantics (from the grilled plan — the table is the contract):

- **`ptt_event` press** → reset the provider's buffer, inject speech-start.
- **`speech_end`** (End Turn) → after a short grace (~200ms, tunable) for in-flight
  audio frames, inject speech-end; the existing loop finalizes the stream, which
  triggers the REST transcription.
- **`ptt_event` release** (Cancel Turn) → reset the buffer, then inject speech-end;
  finalize sees an empty buffer and stays silent. Ignoring release is a bug: the 25s
  segment cap would eventually transcribe cancelled audio and answer unprompted.
- **Empty-result callback** (tap with no intelligible speech) → enqueue the canned
  announcement **"I didn't hear you! Press the button and try again."** through the
  existing announcement queue, so the device gets a normal TTS cycle and unlocks
  instead of freezing 40s in "Thinking…". Cancel Turn must never announce.
- When the active provider is `sarvam_rest`, the TEN VAD pipeline is **not
  constructed** — PTT events are the only turn signal. All other providers keep VAD
  exactly as today.

## Acceptance criteria

- [ ] Press/speech_end/release data messages produce the correct injections and
      buffer resets (table-driven test with the existing fake stream)
- [ ] Cancel Turn path produces no LLM dispatch and no announcement
- [ ] Empty-result path enqueues exactly one didn't-hear announcement
- [ ] TEN VAD absent for `sarvam_rest` sessions, present for others
- [ ] Existing streaming-provider tests still green (`go test ./pkg/livekit/`)

## Blocked by

- 002 (provider's reset-buffer and empty-result extension points must exist)
