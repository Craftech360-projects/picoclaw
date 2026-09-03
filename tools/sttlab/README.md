# STT Lab

A browser-based rig for evaluating VAD and STT providers against the same audio.

Record from the mic (or replay a saved WAV), let a VAD segment it, and send every
segment to Omli STT, Gemini and Sarvam REST at once — same audio, same boundaries,
three transcripts side by side. Built to answer whether Omli's `kids_v5` VAD could
replace `ten_vad` in the voice agent; findings are in
[`docs/omli-vad-evaluation.md`](../../docs/omli-vad-evaluation.md).

## Running it

```bash
cp .env.example .env      # add GEMINI_API_KEY / SARVAM_API_KEY
./run.sh                  # serves on 127.0.0.1:8100
```

Put a TLS reverse proxy in front and browse to it over HTTPS — `getUserMedia`
refuses to run outside a secure context, so the mic will not work over plain
`http://` on anything but localhost.

## What it does

Audio streams to Omli in `stt_vad_backend=kids_v5` mode. Its `vad_event`
start/stop drive segmentation; the transcripts on that socket are ignored. Each
segment is cut from a locally-held buffer and sent to all three providers.

Two deliberate departures from the obvious design, both forced by measured
behaviour:

- **Omli's `max_segment_ms` is pinned to 60000 so its cap never fires.** When
  that cap triggers it truncates what it emits but leaves its VAD's utterance
  open, and every sample between the flush and the true speech-end is discarded —
  a whole spoken sentence, measured. `local_cap_ms` replaces it, cutting from a
  buffer we hold, so nothing is lost. A cut made while the speaker is still going
  is flagged and continues as `#n.2`.
- **Omli's STT runs over a second `vad_enabled=true` socket**, one bounded
  utterance per message, because it collapsed a 38s segment to two words while
  being accurate on 5s of the same audio.

Gemini gets a fresh socket per segment. A shared session with one activity window
per segment has to be serialised, and that queueing dropped transcripts under
bursts of short turns.

**Latency is not comparable across the three columns.** Omli streams and reports
its own inference time. Gemini and Sarvam each receive a finished segment, so
their numbers cover connect + upload + inference for the whole utterance.

## Files

| | |
|---|---|
| `app.py` | the server: VAD plumbing, segment cutting, all three providers |
| `static/index.html` | the UI: timeline, per-segment audio, three transcript columns |
| `selfcheck.py` | end-to-end smoke test — `python3 selfcheck.py omli gemini sarvam both` |
| `sweep.py` | replay a WAV once per VAD config: `sweep.py take.wav 600:20000:1000:0.4` (min_silence:max_segment:hangover:confidence) |
| `gemprobe.py` | dump raw Gemini Live frames for one WAV, for debugging that leg alone |

Every session's raw mic stream is saved to `recordings/<id>.wav` so one live take
can be replayed through `sweep.py` at many settings — four separate live takes are
not comparable to each other, one replayed take is.

`recordings/` and `segments/` are gitignored: they are large and contain
recordings of real children.

## Related

`cmd/tenvad-probe` runs the worker's own `ten_vad` pipeline over the same WAVs,
so the two VADs can be compared on identical audio.
