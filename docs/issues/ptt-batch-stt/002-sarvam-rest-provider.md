# 002 — sarvam_rest STT provider: buffer → WAV → REST → final event

**Type:** AFK · **Status:** closed · **Assignee:** claude
**Spec / Plan:** `docs/plan-stt-ptt-batch.md` (P1) · ADR 0007
**Repo:** picoclaw (branch `stt-ptt-batch`)

## What to build

A new STT provider, `sarvam_rest`, registered alongside the existing providers and
selected by the manager's active-provider flip like any other. It implements the
standard streaming interface but transcribes per-utterance:

- Audio sent to the stream is **buffered, not forwarded** — capped at 30s of 16kHz
  mono PCM (the REST endpoint's limit); past the cap, drop and warn once per segment.
- **Finalize with an empty buffer is a silent no-op** — this is the Cancel Turn path
  (see glossary) and must produce no event and no announcement.
- **Finalize with buffered audio** WAV-encodes it and POSTs to Sarvam's sync
  speech-to-text endpoint (model/language/key all arrive via the existing provider
  config — model `saaras:v4`, `language_code=unknown`; `mode=transcribe` sent or
  omitted per issue 001's answer). One retry on failure, ~10s timeout, and the HTTP
  call runs off the caller's goroutine so the pipeline event loop is never blocked.
  On success, emit exactly one final transcript event carrying the transcript and the
  detected language.
- **Real audio but empty/blank transcript** (the empty-tap case) fires an
  empty-result callback instead of emitting an event — consumed by issue 003.
- Two small extension points, discoverable by type assertion: reset-buffer (discard
  accumulated audio) and set-empty-result-handler.
- REST failing both attempts emits nothing; the pipeline's existing finalize-timeout
  reset covers it.

Name note: `sarvam_rest`, not `sarvam_batch` — Sarvam's "Batch API" is a different
async product.

**Prior art — start from it, don't rewrite:** branch `feat/sarvam-rest-stt` (commit
`c99052b`, not in this branch's ancestry) carries a working REST adapter + httptest
suite with the async-flush and close-race concurrency already correct. Bring those two
files over and adapt: it selects via a transport env var inside the `sarvam` provider
(replace with a standalone factory-registered provider), and lacks reset-buffer, the
empty-result callback, the 30s cap, and the retry. It also sends a `sample_rate` form
field not present in current API docs — issue 001 confirms or drops it.

## Acceptance criteria

- [x] Unit tests (httptest): happy path buffer→WAV→POST→one final event with language
- [x] Empty-buffer finalize: no event, no callback, no HTTP call
- [x] Real-audio→blank-transcript: callback fires, no event
- [x] Cap enforced; oversize audio dropped with a single warning
- [x] Retry-then-fail: no event, no panic, stream reusable for the next turn
- [x] Reset-buffer discards audio so the next finalize sees only new audio
- [x] Provider resolvable through the factory by name with model/language/key from config
- [x] `go test ./pkg/voice/stt/` green

## Blocked by

- 001 (settles the `mode` field and confirms quality is worth building on) — closed

## Resolution

Shipped in `0d29287` (picoclaw). `pkg/voice/stt/sarvam_rest_provider.go` (new) +
`sarvam_rest_provider_test.go` (10 tests, all passing) + two-line `factory.go`
registration. Adapted from the `feat/sarvam-rest-stt` prior art per the ticket's
pointer, restructured as a standalone `sarvam_rest` provider (was a transport
switch inside `sarvam`) and extended with `ResetBuffer`, the empty-result
callback, the 30s cap, and single-retry-on-5xx.

Every acceptance box maps to a named test: `TestSarvamRESTSendsTheDocumentedRequest`,
`TestSarvamRESTFinalizeWithNoAudioDoesNotCall`, `TestSarvamRESTBlankTranscriptFiresEmptyHandler`,
`TestSarvamRESTBufferCap`, `TestSarvamRESTRetryThenFailLeavesStreamUsable`,
`TestSarvamRESTResetBufferDiscardsAudio`, `TestSarvamRESTResolvableThroughFactory`.
`mode`/`sample_rate` handling matches issue 001's findings exactly (mode sent,
sample_rate dropped).

**Two-round self-review** (3 parallel Sonnet reviewers → fix → 1 verifier) found
and fixed:
- A `Finalize`/`Close` TOCTOU that could spawn a transcribe goroutine after
  `resultChan` closed, panicking the process on participant-disconnect timing.
  Fixed by moving the closed-check and `inFlight.Add` under the same mutex
  `Close` uses to gate its `Wait`.
- An undocumented 0.5s floor silently dropping the empty-tap callback for
  brief taps — removed.
- `SupportsStreaming: true` on a provider that emits exactly one event per
  utterance — corrected to `false`.
- Callback-goroutine contract (fires on the transcription goroutine, not the
  pipeline loop) was undocumented — now stated on `SetEmptyResultHandler`.

**Deferred, not fixed:** overlapping `Finalize` calls (25s hard-cap racing a
fast `speech_end`) can emit two final events out of speech order — flagged
with a `ponytail:` comment at the call site rather than built out, since
nothing yet shows it happens with real 25s+ utterances in Manual Talk. Revisit
if 005's live E2E or later logs show it.

`go vet` and `go build ./pkg/voice/stt/...` clean. `-race` unavailable locally
(cgo toolchain broken, pre-existing, unrelated to this change) — concurrency
fix instead verified by an independent reviewer agent's disposable stress test
(5000 iterations, zero panics) before this commit; not part of the repo.

Unblocks 003 (agent wiring — press/speech_end/release → these APIs).
