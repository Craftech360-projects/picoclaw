"""Dump every raw frame Gemini sends for one segment, to see where transcribe() stalls."""
import asyncio
import base64
import json
import os
import sys
import time
import wave

import websockets

URL = ("wss://generativelanguage.googleapis.com/ws/"
       "google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent")
KEY = os.environ.get("GEMINI_API_KEY", "").strip()
MODEL = os.environ.get("GEMINI_MODEL", "gemini-3.5-transcribe-live")


async def main(path):
    print("key present: %s (len %d)   model: %s" % (bool(KEY), len(KEY), MODEL))
    if not KEY:
        sys.exit("no GEMINI_API_KEY in env")
    with wave.open(path, "rb") as wf:
        pcm = wf.readframes(wf.getnframes())
    print("segment: %.2fs" % (len(pcm) / 2 / 16000))

    ws = await websockets.connect(f"{URL}?key={KEY}", max_size=2**24)
    await ws.send(json.dumps({"setup": {
        "model": f"models/{MODEL}",
        "generationConfig": {"responseModalities": ["TEXT"]},
        "inputAudioTranscription": {"languageCodes": [], "mode": "SMART"},
        "realtimeInputConfig": {"automaticActivityDetection": {"disabled": True}},
    }}))
    print("setup ack:", (await asyncio.wait_for(ws.recv(), 15))[:400])

    t0 = time.time()
    await ws.send(json.dumps({"realtimeInput": {"activityStart": {}}}))
    for i in range(0, len(pcm), 32000):
        await ws.send(json.dumps({"realtimeInput": {"audio": {
            "data": base64.b64encode(pcm[i:i + 32000]).decode(),
            "mimeType": "audio/pcm;rate=16000"}}}))
    await ws.send(json.dumps({"realtimeInput": {"activityEnd": {}}}))
    print("audio sent, waiting for frames...")

    while True:
        try:
            raw = await asyncio.wait_for(ws.recv(), 20)
        except asyncio.TimeoutError:
            print("TIMEOUT — no frame in 20s")
            break
        s = raw.decode() if isinstance(raw, bytes) else raw
        print("  +%4dms  %s" % (int((time.time() - t0) * 1000), s[:400]))
    await ws.close()


asyncio.run(main(sys.argv[1]))
