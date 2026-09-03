"""Feed a WAV through the app the way the browser does; assert the pipeline fires.

    python3 selfcheck.py omli
    python3 selfcheck.py both      # needs GEMINI_API_KEY in .env
"""
import asyncio
import json
import sys
import wave

import websockets

SR, CHUNK = 16000, 512
CLIP = "/root/omli-stt-test/clips/clip_2.wav"


async def run(mode):
    with wave.open(CLIP, "rb") as wf:
        pcm = wf.readframes(wf.getnframes())
    pcm += b"\x00\x00" * int(SR * 2.0)  # trailing silence so the VAD sees speech-end
    seen = {"vad_start": 0, "vad_stop": 0, "segment": 0,
            "omli": 0, "gemini": 0, "sarvam": 0, "err": 0}

    async with websockets.connect("ws://127.0.0.1:8100/ws", max_size=2**24) as ws:
        await ws.send(json.dumps({
            "mode": mode, "confidence": 0.4, "pre_pad_ms": 600, "post_pad_ms": 200,
            "min_silence_ms": 300, "hangover_ms": 700, "max_segment_ms": 12000,
        }))

        async def recv():
            async for raw in ws:
                m = json.loads(raw)
                kind = m.get("type")
                if kind == "vad":
                    seen["vad_" + m["event"]] += 1
                elif kind == "segment":
                    seen["segment"] += 1
                    print("  segment #%s  %s ms  %s" % (m["seq"], m["duration_ms"], m["wav"]))
                elif kind == "transcript":
                    # .get guard: an unknown source used to raise KeyError here,
                    # killing this task and silently swallowing every later
                    # message - which looked exactly like a broken provider.
                    seen[m["source"]] = seen.get(m["source"], 0) + 1
                    print("  [%s] %r  (%s ms)" % (m["source"], m["text"], m["latency_ms"]))
                elif kind == "error":
                    seen["err"] += 1
                    print("  ERROR %s: %s" % (m["source"], m["message"][:150]))

        task = asyncio.create_task(recv())
        for i in range(0, len(pcm), CHUNK * 2):
            await ws.send(pcm[i:i + CHUNK * 2])
            await asyncio.sleep(CHUNK / SR)
        await asyncio.sleep(8)
        task.cancel()

    print("  -> %s" % seen)
    assert seen["vad_start"] >= 1, "VAD never detected speech"
    assert seen["vad_stop"] >= 1, "VAD never ended a segment"
    assert seen["segment"] >= 1, "no segment was cut from the local buffer"
    if mode in ("omli", "both"):
        assert seen["omli"] >= 1, "no Omli transcript"
    if mode in ("gemini", "both"):
        assert seen["gemini"] >= 1, "no Gemini transcript"
    if mode in ("sarvam", "both"):
        assert seen["sarvam"] >= 1, "no Sarvam transcript"


for mode in sys.argv[1:] or ["omli"]:
    print("== mode=%s" % mode)
    asyncio.run(run(mode))
    print("== mode=%s PASS\n" % mode)
