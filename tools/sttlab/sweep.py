"""Stream one WAV through the app once per VAD config and print what the VAD did
with it: how many segments, how long each was, and what each STT made of them.

    python3 sweep.py kid.wav 600 700                        # min_silence only
    python3 sweep.py kid.wav 600:15000 600:20000            # + max_segment
    python3 sweep.py kid.wav 600:15000:500 600:15000:1000   # + hangover
"""
import asyncio
import json
import sys
import wave

import websockets

SR, CHUNK = 16000, 512


async def run(path, min_silence, max_seg, hangover, confidence):
    with wave.open(path, "rb") as wf:
        assert wf.getframerate() == SR and wf.getnchannels() == 1
        pcm = wf.readframes(wf.getnframes())
    pcm += b"\x00\x00" * int(SR * 2.5)  # trailing silence so the last segment can end

    segs = {}
    order = []

    async with websockets.connect("ws://127.0.0.1:8100/ws", max_size=2**24) as ws:
        await ws.send(json.dumps({
            "mode": "both", "confidence": confidence, "pre_pad_ms": 600, "post_pad_ms": 200,
            "min_silence_ms": min_silence, "hangover_ms": hangover,
            "max_segment_ms": max_seg,
        }))

        async def recv():
            async for raw in ws:
                m = json.loads(raw)
                seq = m.get("seq")
                if seq is not None and seq not in segs:
                    segs[seq] = {"dur": None, "capped": False}
                    order.append(seq)
                if m["type"] == "segment":
                    segs[seq]["dur"] = m["duration_ms"]
                    segs[seq]["capped"] = m.get("capped", False)
                elif m["type"] == "transcript":
                    segs[seq][m["source"]] = m["text"]
                    segs[seq][m["source"] + "_ms"] = m["latency_ms"]
                elif m["type"] == "error":
                    print("   ERROR %s: %s" % (m["source"], m["message"][:120]))

        task = asyncio.create_task(recv())
        for i in range(0, len(pcm), CHUNK * 2):
            await ws.send(pcm[i:i + CHUNK * 2])
            await asyncio.sleep(CHUNK / SR)
        await asyncio.sleep(6)
        task.cancel()

    print("")
    print("=== min_silence=%d max_segment=%d hangover=%d confidence=%.2f  ->  %d segment(s)"
          % (min_silence, max_seg, hangover, confidence, len(order)))
    for seq in order:
        s = segs[seq]
        print("  #%-3s %6s ms%s" % (seq, s["dur"], "  [CAP]" if s["capped"] else ""))
        for src in ("omli", "gemini", "sarvam"):
            if src in s:
                print("      %-6s: %r  (%s ms)" % (src, s[src], s.get(src + "_ms")))
    return len(order)


path = sys.argv[1]
counts = {}
# args: min_silence[:max_segment[:hangover[:confidence]]]   e.g. 600:20000:1000:0.6
for arg in sys.argv[2:] or ["600"]:
    parts = (arg.split(":") + ["", "", ""])[:4]
    ms = int(parts[0])
    mx = int(parts[1] or 20000)
    hg = int(parts[2] or 1000)
    cf = float(parts[3] or 0.4)
    counts[arg] = asyncio.run(run(path, ms, mx, hg, cf))
print("")
print("=== segments per config: %s" % counts)
