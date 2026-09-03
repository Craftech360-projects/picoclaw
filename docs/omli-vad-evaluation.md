# Omli `kids_v5` VAD — Evaluation Result

**Date:** 2026-09-03
**Question:** Can Omli's VAD replace `ten_vad` as picoclaw's hands-free turn detector?
**Verdict:** **Not as a drop-in today.** Its speech *detection* is good; its speech *end* detection is not, and end detection is what decides when the toy speaks.

Test rig: <https://stt.64-227-170-31.sslip.io> (pm2 `sttlab` on the dev box). All raw takes replayable from `/root/sttlab/recordings/`.

---

## What was tested

| Take | Source | Length | Content |
|---|---|---|---|
| `h1`, `h2` | Hitansh (child, ~7y) | 40s / 18.5s | reciting a memorised speech |
| `5ead14df8a69` | adult, normal room | 53s | short sentences with pauses |
| `976eb80efe1b` | adult, deliberate silences | 68s | sentence → 15s still → short word → still |
| `1be2a3369d57` | adult, rapid short turns | 41s | 9 back-to-back utterances |
| `c65f3f113e91` | adult, sparse | 80s | 7.3s speech in 80s (9%) |

Each take streams to Omli in `stt_vad_backend=kids_v5` mode; its `vad_event` start/stop drive segmentation, and each segment goes to Omli STT, Gemini (`gemini-3.5-transcribe-live`) and Sarvam REST for cross-checking.

---

## Strength: it does not miss speech

This is real and it matters. **Across every take, no real utterance failed to trigger a segment.**

- `1be2a3369d57`: 9 segments totalling 42.2s against a 41.5s recording — full coverage, no gaps.
- `c65f3f113e91`: all 14 real speech bursts fell inside a segment.
- The child clips triggered on quiet, hesitant, accented speech that a generic VAD would plausibly miss.

High recall is exactly what a kids VAD should optimise for, and Omli delivers it.

## Weakness: it does not know when speech ends

Measured against the actual acoustic end of speech (100ms RMS windows, speech = runs ≥300ms above 20th-percentile floor + 12dB):

| Take | Speech ended | VAD fired stop | Lag |
|---|---|---|---|
| `976eb80efe1b` #0 | 3.30s | 7.10s | **3.80s** |
| `976eb80efe1b` #1 | 23.80s | 27.10s | **3.30s** |
| `976eb80efe1b` #2 | 62.60s | 63.94s | **1.34s** |
| `clip_2` (clean) | 3.84s | ~4.61s | **0.77s** |

Configured `min_silence_ms` was 600. In a quiet room the lag is still 1.3–3.8s.

## Weakness: it triggers on non-speech

On `c65f3f113e91` (80s, only 7.3s of actual speech):

- **14 segments produced, covering ~82s** — it segmented essentially the whole recording.
- **10 of 14 returned empty from all three STT providers independently.**
- Five triggers were on 0.3–0.5s transients — breaths, clicks, a chair.
- Segment #1: **15s of audio containing one word.**

## Defect: `confidence` is a no-op

Replayed one take at `confidence` = 0.05, 0.3, 0.4, 0.5, 0.6, 0.7, 0.95. **Byte-identical segmentation at every value** (19976 / 10816 / 20288 ms). The other four parameters verifiably change behaviour through the same URL, so this is that field specifically being ignored server-side.

Consequence: the one knob that would trade recall for precision — the exact trade we need — is unavailable.

## Defect: `max_segment_ms` force-flush discards audio

When Omli's own cap fires it truncates what it emits but leaves its VAD's utterance open. Everything between the flush and the true speech-end is dropped.

On `976eb80efe1b` at cap 20000: segment stop at 27.10s, next start at 44.58s, **nothing in between**. That 17.5s window contained a full sentence — extracted and transcribed by Gemini as *"Am I audible to you? I'm just checking if everything is working or not."* Replaying the same file at cap 60000 produced one 37.2s segment covering it, confirming the VAD's real speech-end was always ~44.3s.

**Worked around client-side:** pin Omli's cap to 60000 so it never fires, and apply our own cap against a locally-held buffer. Verified — the lost sentence now lands in row `#1.2`. This fix is in the test rig and ports directly to picoclaw.

## Related: Omli's STT collapses long segments

Not a VAD issue, but it compounds the above. A 37.2s segment returned `आई थिंक` ("I think"). Gemini on identical audio returned the full three sentences. Omli STT is accurate on ~5s input.

**Worked around client-side:** drive Omli STT over a second `vad_enabled=true` socket, one bounded utterance per message, instead of taking transcripts off the streaming socket.

---

## Measured comparison with `ten_vad`

`ten_vad` was run over the identical WAVs via `cmd/tenvad-probe`, using the worker's own
`VADPipeline` at production defaults (threshold 0.7, endpoint 1000ms, minSpeech 300ms, hop 256).

| Take | Real utterances | `ten_vad` segments | Omli segments | Omli empty |
|---|---|---|---|---|
| `c65f3f113e91` (80s, 9% speech) | 4 | **4** | 14 | **10** |
| `1be2a3369d57` (41s, rapid turns) | ~7 | 7 | 9 | 4 |
| `976eb80efe1b` (68s) | ~6 | 5 | 3 | 0 (but **lost a sentence**) |
| `h1` (child, 40s) | — | 6 | 5 | 0 |
| `h2` (child, 18.5s) | — | 2 | 3 | 1 |

### Precision: `ten_vad` wins decisively

On `c65f3f113e91`, `ten_vad` produced exactly four segments, and they land on exactly the four real
utterances (0.72-4.37s, 20.53-22.00s, 58.08-60.75s, 70.56-71.98s). Omli produced fourteen, of which
ten were transcribed as empty by all three STT providers independently. **Phantom-turn rate: 0/4 vs
10/14.**

### Endpointing: `ten_vad` wins decisively

`ten_vad`'s longest segment on adult takes is 4.24s and it closes promptly after speech. Omli's
run to 15-20s and are routinely padded with many seconds of silence — its 15s segment containing a
single word has no counterpart in the `ten_vad` output.

### `ten_vad` caught the audio Omli lost

The sentence Omli discarded via the cap force-flush (28.0-34.7s of `976eb80efe1b`) is covered by
`ten_vad` segments #2 and #3 (28.32-30.90s, 31.49-35.68s). No workaround required.

### Where `ten_vad` is weaker

It misses short bursts. On `976eb80efe1b` it produced nothing for the bursts at 6.1-6.8s,
23.3-23.8s, 47.0-47.8s and 52.4-52.7s. Some of those are the non-speech transients Omli also
wasted segments on — but at least one is real: Omli transcribed the 23.3s region as "I think".
`minSpeechMS=300` in the pipeline discards anything shorter, so very short answers ("haan", "yes")
are the risk case. **This is the one dimension where Omli's higher recall could matter, and it is
the dimension our sample tests worst.**

### Architectural difference (independent of quality)

| | `ten_vad` | Omli `kids_v5` |
|---|---|---|
| Location | in-process (cgo) on the worker | remote websocket |
| Added latency | none | ~75ms RTT + endpointing lag |
| Failure mode | process-local | network partition kills turn detection |
| Bandwidth | none | full 16kHz PCM upstream, always |
| Tunable | threshold + endpoint ms | `confidence` ignored; others work |

### Note found while building the probe

[`pipeline.go:87`](../pkg/voice/vad/pipeline.go#L87) measures endpoint silence in **wall-clock**
time (`time.Since`), not audio time. Live, the two coincide, so this is not a production bug - but
endpointing is therefore sensitive to frame-delivery jitter rather than to the audio itself, and any
offline replay must be paced at 1x or every file collapses into one never-closing segment.

## Impact if adopted as-is

picoclaw feeds both PTT and `ten_vad` into the same `vad.VADEvent` channel ([`room_session.go:562`](../pkg/livekit/room_session.go#L562), [`:905`](../pkg/livekit/room_session.go#L905)), so a swap is mechanically simple. The behavioural consequences are not:

1. **Phantom turns.** [`audio_pipeline.go:936`](../pkg/livekit/audio_pipeline.go#L936) answers an empty transcript with *"I didn't hear you! Press the button and try again."* Correct for an empty button tap; under VAD, the `c65f3f113e91` take would have fired it **10 times in 80 seconds**, unprompted, into a silent room.
2. **Response latency.** 1.3–3.8s of endpointing sits in front of STT + LLM + TTS. Total wait 4–6s.
3. **Corrupted context.** Empty/garbage turns enter conversation history and burn tokens.
4. **Cost.** ~3x the real STT traffic on the sparse sample.
5. **No barge-in.** A VAD that holds segments open through silence cannot detect a child interrupting.

---

## Recommendation

1. **Do not replace `ten_vad` with the hosted Omli VAD.** Measured head-to-head on identical audio, `ten_vad` is better on both axes that matter: 0 phantom segments vs 10, and prompt endpointing vs 15-20s. It also caught the sentence Omli lost. The swap would be a regression.
2. **Take Omli up on the on-device VAD.** Its recall on children's speech is the genuinely hard part and Omli has it. On-device removes the network hop, the bandwidth, and the dependency. Re-run this evaluation against it.
3. **Report to Omli:** (a) `confidence` ignored, (b) cap force-flush discards audio, (c) stop lag. All three reproduce from `/root/sttlab/recordings/976eb80efe1b.wav`.
4. **Keep the client-side fixes** (local cap; bounded-utterance STT socket) regardless of which VAD wins — both are VAD-agnostic.

---

## Limitations of this evaluation

State these before quoting the numbers:

- **Sample is small and mostly adult.** Four of six takes are one adult male voice. The only child audio is two clips of a single ~7-year-old *reciting a memorised speech* — not conversational, not multi-child, no age range.
- **No device audio.** Everything was captured through a laptop mic in one room. The toy's mic, its noise floor, and its far-field distance are a materially different acoustic problem, and one device capture we tried (`part0003`) contained no intelligible speech at all.
- **Short-utterance recall is untested.** `ten_vad`'s `minSpeechMS=300` discards very short sounds, and our takes contain few one-word answers. A child answering "haan" is the case most likely to favour Omli, and we have almost no samples of it.
- **`confidence` being inert may be a deployment bug** rather than intended, in which case several conclusions here soften considerably.
