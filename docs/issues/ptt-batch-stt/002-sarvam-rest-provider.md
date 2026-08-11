# 002 — sarvam_rest STT provider: buffer → WAV → REST → final event

**Type:** AFK · **Status:** open
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

- [ ] Unit tests (httptest): happy path buffer→WAV→POST→one final event with language
- [ ] Empty-buffer finalize: no event, no callback, no HTTP call
- [ ] Real-audio→blank-transcript: callback fires, no event
- [ ] Cap enforced; oversize audio dropped with a single warning
- [ ] Retry-then-fail: no event, no panic, stream reusable for the next turn
- [ ] Reset-buffer discards audio so the next finalize sees only new audio
- [ ] Provider resolvable through the factory by name with model/language/key from config
- [ ] `go test ./pkg/voice/stt/` green

## Blocked by

- 001 (settles the `mode` field and confirms quality is worth building on)
