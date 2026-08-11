# 003 — Agent wiring: PTT signals drive the turn, no VAD, didn't-hear announcement

**Type:** AFK · **Status:** closed · **Assignee:** claude
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

- [x] Press/speech_end/release data messages produce the correct injections and
      buffer resets (table-driven test with the existing fake stream)
- [x] Cancel Turn path produces no LLM dispatch and no announcement
- [x] Empty-result path enqueues exactly one didn't-hear announcement
- [ ] TEN VAD absent for `sarvam_rest` sessions, present for others — **predicate
      tested, construction branch not** (see Resolution)
- [x] Existing streaming-provider tests still green (`go test ./pkg/livekit/`)

## Blocked by

- 002 (provider's reset-buffer and empty-result extension points must exist) — closed

## Resolution

Shipped in `4b3691a` (picoclaw). `pkg/livekit/room_session.go` (data-channel cases
+ helpers), `pkg/livekit/audio_pipeline.go` (didn't-hear fallback + turn-generation
counters), `pkg/livekit/ptt_turn_test.go` (new, 18 tests).

`RunInbound` is genuinely untouched, as the ticket required — PTT events are
injected into the same channel real TEN VAD events already flowed through, so the
inbound loop cannot tell them apart. TEN VAD is skipped at construction for
`sarvam_rest`; the channel is created either way.

**Three review rounds** (3 parallel reviewers, then a verifier, then a second
verifier). Six real defects found and fixed:

1. `ps.sttStream`/`ps.turnEvents` read without `ps.mu` while written under it —
   a data race against the file's own convention, plus a tap arriving before the
   track finished subscribing vanished with no log. Now behind a locked
   `turnPlumbing()` accessor, with a warning on the dropped path.
2. The gateway maps *every* non-`"start"` listen state to `action:"release"`,
   including the `"detect"` that client.py sends to kick the greeting and on its
   stalled-TTS retry — so a greeting kick would have wiped a live turn. Now
   requires the firmware's actual `state:"stop"`.
3. Cancel Turn could still announce: trailing RTP frames repopulated the buffer
   after the reset, producing a real (empty-transcript) REST call. Generation
   counters now gate the announcement.
4. A stale empty-result from a superseded utterance could announce over a newer
   turn — same mechanism.
5. Cancel Turn could still *dispatch to the LLM* for the same trailing-frames
   reason — Cheeko answering a cancelled question, a direct acceptance-criteria
   violation. Buffer is now wiped on both sides of the grace window.
6. **Deadlock**: teardown holds `participant.mu` across `sttStream.Close()`, which
   waits for the in-flight transcription goroutine — the same goroutine my
   empty-result callback ran on, needing that lock to speak. A permanent hang of
   `leave()` (guarded by `sync.Once`, so no retry). The callback is now spawned.

**Known gap, not fixed, commented at the counters:** two turns finalized inside
one REST round-trip (press/end/press/end in ~1s) share a single announce slot, so
the first turn's empty result can announce as the second's. Costs one spurious
"I didn't hear you"; the child presses again. The correct fix is per-utterance
identity through the provider's callback — the same missing correlation issue 002
deferred, worth doing once for both rather than papering over here.

**Unverified criterion:** "TEN VAD absent for `sarvam_rest`, present for others"
is proven for the `isPTTDrivenProvider` predicate but not for the construction
branch inside `handleTrackSubscribed`, which needs real `*webrtc.TrackRemote` /
`*lksdk.RemoteParticipant` objects and has no test harness in this package. A
future refactor could invert that condition with the suite staying green. Building
a webrtc harness was out of proportion here; 005's live session covers it in
practice (the log line "TEN VAD bypassed: PTT-driven provider active" is the
check).

`go vet` clean; `go test ./pkg/livekit/` and `./pkg/voice/stt/` green.
`TestSynthesizeAndPlayLogsTTSProviderType` fails identically on the pre-change
tree (verified by `git stash`) — pre-existing, unrelated, skipped in runs.
`-race` still unavailable locally (cgo toolchain, same as issue 002).

Unblocks 005 (live E2E) — its last blocker.
