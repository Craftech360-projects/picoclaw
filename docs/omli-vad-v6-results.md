# Omli `kids_v6` VAD — Test Results

**Date:** 2026-09-07
**Question:** Is `kids_v6` better than `kids_v5`, and what should the parameters be?
**Short answer:** Yes on conversation, clearly. The recommended settings below are measured, not inherited.

Rig: <https://stt.64-227-170-31.sslip.io> (pm2 `sttlab`). Every result replays from
`/root/sttlab/recordings/`. Each configuration is replayed at 1x through the same
pipeline, so runs are comparable to each other rather than to separate live takes.

---

## Recommended settings

```
vad_backend    kids_v6
confidence     0.4        (no effect - see below)
pre_pad_ms     600        untested
post_pad_ms    200        untested
min_silence_ms 600        measured: better than 700 and 800
hangover_ms    1000       measured: better than 2000; ~equal to 700
max_segment_ms 12000      enforced client-side, NOT sent to Omli
```

---

## v6 vs v5 on real conversation

`c06d58a80bf2.wav` — 3m44s, an adult questioning a 4-year-old, many one-word answers.
Identical audio, identical parameters, only the backend changed.

| | kids_v5 | kids_v6 |
|---|---|---|
| Segments | 33 | **37** |
| Cap-flushed at 12s | 4 | **2** |
| All-three-STTs empty | 1 | 2 |
| **Short segments (<=2.5s) containing speech** | **2** | **17** |

That last row is the result. Both versions *heard* every answer; v6 keeps them as
separate turns instead of wrapping each in a long segment:

| The child said | v5 segment | v6 segment |
|---|---|---|
| "Chocolate." | 5376 ms | **1792 ms** |
| "Orange." | 3968 ms | **2048 ms** |
| "Water." | - | **1760 ms** |
| "I'm four years." | - | **2880 ms** |

v5 also merged question and answer into one block - *"Okay, what is your name? Hitan."* -
where v6 kept them apart.

### Confirmed independently

Re-run through **Omli's own `stream_integration_test.py`**, so none of our code is
involved:

| | kids_v5 | kids_v6 |
|---|---|---|
| VAD starts / stops | 29 / 29 | **34 / 34** |
| Transcripts with text | 20 | **28** |
| Empty results | 9 | **6** |

More segments *and* fewer empties - the extra segments are speech, not noise.

---

## Where v6 was worse

On `4e96c076dcd5.wav` (94s, two children reciting a memorised speech, few real gaps)
v6 went the other way:

| | kids_v5 | kids_v6 |
|---|---|---|
| Segments | 26 | 10 |
| Cap-flushed | 0 | 4 |

One 15s v6 segment came back as `लगा` repeated 32 times - Omli's STT collapsing on
merged input.

The two recordings differ in a way that explains it: the conversation has genuine
pauses between turns, the recitation does not. Read that way v6 is *more* reluctant
to cut mid-speech and *more* decisive at real boundaries, which is the behaviour we
want - but a child who talks continuously will still produce long segments.

---

## Parameter sweeps (all on `c06d58a80bf2.wav`, kids_v6)

### min_silence_ms - 600 wins on every measure

| min_silence | segments | cap-flushed | short segs with speech | all-empty |
|---|---|---|---|---|
| **600** | **39** | **4** | **18** | **1** |
| 700 | 36 | 5 | 14 | 2 |
| 800 | 34 | 5 | 14 | 2 |

Raising it loses 4 short answers and *increases* empties. A hypothesis that 700 would
reduce empties (it did on v5) does not transfer: v6 had already fixed the empty rate.

### hangover_ms - avoid 2000

| hangover | segments |
|---|---|
| 700 | 41 |
| **1000** | **39** |
| 2000 | 33 |

At 2000 no segment may close before it is 2s old, so a child's half-second "yes"
cannot end a turn and is glued to the adult's next question. Measured at 2000 on this
file: one Omli segment ran **49 seconds** and swallowed four exchanges.

700 yields 2 more segments than 1000 and is defensible; 1000 is kept as the safer
side of a range whose ends we have tested.

### confidence - not re-tested on v6

On v5 it was a **no-op**: 0.05 and 0.95 gave byte-identical segmentation. Not
re-verified on v6. Treat as unknown until it is.

---

## Latency, with providers streamed from `vad start`

Median over the 37 segments, measured as the wait **after** speech ends:

| | median | max |
|---|---|---|
| Omli | 393 ms | 1150 ms |
| Gemini | 402 ms | 2837 ms |
| Sarvam | **215 ms** | 3003 ms |

Sarvam frequently finalises *before* the child stops talking. Note Omli reports its
own inference time while the other two are our end-to-end measurement, so these are
not strictly like for like.

**Known defect in our rig:** roughly 3% of turns lose a provider transcript to
`timed out during opening handshake`, because each turn dials a fresh socket. A retry
on open would fix it.

---

## Carried over from v5, not re-verified on v6

- **`max_segment_ms` force-flush discards audio.** When Omli's own cap fires it
  truncates what it emits but leaves its VAD's utterance open, and everything between
  the flush and the true speech-end is dropped - a full spoken sentence, measured on
  v5. We therefore pin Omli's cap to 60000 and enforce our own against a local
  buffer. Whether v6 still does this is untested.
- **Omli's STT collapses long segments** (38s returned two words on v5). v6's
  tighter segmentation makes it rarer, and the `vad_enabled=true` workaround socket
  has been removed on that basis.

---

## What this does not tell you

- **One child, one room, one microphone.** A laptop mic at desk distance, not the
  toy's mic in a living room.
- **Segments are not conversational turns.** The rig transcribes every segment
  independently. A real STT -> LLM -> TTS loop would need turn assembly on top, or
  it fires the LLM once per segment - 39 times on this recording.
- **No echo.** Every recording was made with nothing playing back. Hands-free
  operation means the toy hears its own voice, which none of this measures.
