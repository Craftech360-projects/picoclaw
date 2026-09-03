"""
STT lab — record from the browser mic, run it through Omli's kids_v5 VAD, and
transcribe each detected segment with Omli's STT, Gemini, or both side by side.

One upstream Omli socket does double duty: it IS the VAD (streaming mode), and
its transcripts are the "Omli STT" column for free.

Gemini is fed the SAME audio at the SAME time: the moment Omli's VAD fires
"start" we open a Gemini activity window, flush the pre-pad out of the local
buffer, and then forward every live chunk to both. Both providers are therefore
transcribing while the child is still speaking, and the latency each reports is
measured from speech-end — which is what the child actually waits.

Run:
    GEMINI_API_KEY=... uvicorn app:app --host 127.0.0.1 --port 8100
"""
import asyncio
import base64
import json
import io
import os
import time
import traceback
import uuid
import wave
from pathlib import Path

import aiohttp
import websockets
from fastapi import FastAPI, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles

SAMPLE_RATE = 16000
BYTES_PER_SAMPLE = 2

OMLI_HOST = os.environ.get("OMLI_HOST", "ext-listen.omli.in:8765")
GEMINI_URL = (
    "wss://generativelanguage.googleapis.com/ws/"
    "google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
)
GEMINI_MODEL = os.environ.get("GEMINI_MODEL", "gemini-3.5-transcribe-live")
GEMINI_API_KEY = os.environ.get("GEMINI_API_KEY", "").strip()

SARVAM_URL = os.environ.get("SARVAM_URL", "https://api.sarvam.ai/speech-to-text")
SARVAM_MODEL = os.environ.get("SARVAM_MODEL", "saaras:v4")
SARVAM_API_KEY = os.environ.get("SARVAM_API_KEY", "").strip()

BASE = Path(__file__).parent
SEG_DIR = BASE / "segments"
SEG_DIR.mkdir(exist_ok=True)
REC_DIR = BASE / "recordings"
REC_DIR.mkdir(exist_ok=True)

# ponytail: buffer the last 120s only. A segment longer than that is already
# past max_segment_ms (12s default), so nothing real gets truncated.
BUFFER_LIMIT_SAMPLES = SAMPLE_RATE * 120

app = FastAPI()


def ms_to_samples(ms: float) -> int:
    return int(SAMPLE_RATE * ms / 1000)


def write_wav(path: Path, pcm: bytes) -> None:
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(BYTES_PER_SAMPLE)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm)


class AudioBuffer:
    """Rolling PCM buffer addressed by absolute sample offset.

    Offsets never restart, so a vad_event mark taken at one moment still points
    at the right audio after the head has been trimmed away.
    """

    def __init__(self) -> None:
        self._buf = bytearray()
        self._head = 0      # absolute sample index of _buf[0]
        self.total = 0      # absolute samples appended so far

    def append(self, pcm: bytes) -> None:
        self._buf += pcm
        self.total += len(pcm) // BYTES_PER_SAMPLE
        overflow = (self.total - self._head) - BUFFER_LIMIT_SAMPLES
        if overflow > 0:
            del self._buf[: overflow * BYTES_PER_SAMPLE]
            self._head += overflow

    def slice(self, start: int, end: int) -> bytes:
        start = max(start, self._head)
        end = min(end, self.total)
        if end <= start:
            return b""
        a = (start - self._head) * BYTES_PER_SAMPLE
        b = (end - self._head) * BYTES_PER_SAMPLE
        return bytes(self._buf[a:b])


async def gemini_transcribe(pcm: bytes) -> tuple[str, int]:
    """One fresh socket per segment.

    Deliberately NOT a shared long-lived session. A single socket carrying one
    activity window per segment has to be serialised, and under a burst of short
    back-to-back turns that queueing swallowed transcripts — a segment Omli and
    Sarvam both read as "Yes, it is working" came back empty after 5.4s.
    Independent sockets run in parallel and cannot interfere with each other.

    The cost of that trade: Gemini no longer sees audio while it is being
    spoken, so the latency reported here covers connect + upload + inference for
    the whole utterance. It is honest for what this path costs, but it is NOT
    comparable to Omli's streaming number.
    """
    if not GEMINI_API_KEY:
        raise RuntimeError("GEMINI_API_KEY not set on the server")
    t0 = time.time()
    ws = await websockets.connect(f"{GEMINI_URL}?key={GEMINI_API_KEY}", max_size=2**24)
    try:
        await ws.send(json.dumps({"setup": {
            "model": f"models/{GEMINI_MODEL}",
            "generationConfig": {"responseModalities": ["TEXT"]},
            "inputAudioTranscription": {"languageCodes": [], "mode": "SMART"},
            # Omli's VAD owns the boundary; Gemini's own must stay off or it
            # would re-segment audio that is already segmented.
            "realtimeInputConfig": {"automaticActivityDetection": {"disabled": True}},
        }}))
        ack = json.loads(await asyncio.wait_for(ws.recv(), 15))
        if "setupComplete" not in ack:
            raise RuntimeError(f"gemini setup rejected: {json.dumps(ack)[:300]}")

        await ws.send(json.dumps({"realtimeInput": {"activityStart": {}}}))
        for i in range(0, len(pcm), 32000):
            await ws.send(json.dumps({"realtimeInput": {"audio": {
                "data": base64.b64encode(pcm[i:i + 32000]).decode(),
                "mimeType": "audio/pcm;rate=16000",
            }}}))
        await ws.send(json.dumps({"realtimeInput": {"activityEnd": {}}}))

        parts: list[str] = []
        last_interim = ""
        t_final = None
        # Short, but reset by every frame carrying text: a long utterance keeps
        # extending while its interims flow, while a segment Gemini has nothing
        # to say about is over quickly instead of holding the turn.
        deadline = 2.5
        while True:
            try:
                raw = await asyncio.wait_for(ws.recv(), deadline)
            except asyncio.TimeoutError:
                break
            msg = json.loads(raw)
            if "error" in msg or "goAway" in msg:
                raise RuntimeError(json.dumps(msg)[:300])
            sc = msg.get("serverContent") or {}
            interim = (sc.get("interimInputTranscription") or {}).get("text", "")
            if interim.strip():
                last_interim = interim.strip()
                deadline = 2.5
            text = (sc.get("inputTranscription") or {}).get("text", "")
            if text.strip():
                parts.append(text.strip())
                t_final = time.time()
                deadline = 1.2
            if sc.get("turnComplete"):
                break
            if sc.get("generationComplete"):
                # NOT a break: it can land ~400ms before the transcription it
                # belongs to, and breaking here drops that transcript.
                deadline = min(deadline, 1.2)
        out = " ".join(parts) or (last_interim + " …" if last_interim else "")
        return out, int(((t_final or time.time()) - t0) * 1000)
    finally:
        await ws.close()


class OmliUtteranceSTT:
    """Omli's STT in `vad_enabled=true` mode: one binary message = one utterance.

    Used instead of the streaming socket's transcripts because Omli's STT
    collapses long input — a measured 38s segment came back as two words, while
    the same engine is accurate on a 5s one. We keep Omli's VAD deciding the
    boundaries and Omli's STT doing the transcription; we only bound how much
    audio it is asked to swallow at once.
    """

    def __init__(self, session_id: str) -> None:
        self.url = f"ws://{OMLI_HOST}?vad_enabled=true&session_id={session_id}u"
        self.ws = None
        self.lock = asyncio.Lock()

    async def transcribe(self, pcm: bytes) -> tuple[str, int]:
        async with self.lock:
            for attempt in (1, 2):
                try:
                    if self.ws is None:
                        self.ws = await websockets.connect(self.url, max_size=2**24)
                    t0 = time.time()
                    await self.ws.send(pcm)
                    while True:
                        msg = json.loads(await asyncio.wait_for(self.ws.recv(), 20))
                        if msg.get("is_final"):
                            return ((msg.get("text") or "").strip(),
                                    int((time.time() - t0) * 1000))
                except (websockets.ConnectionClosed, asyncio.TimeoutError):
                    # The server drops idle connections after ~40s. Reconnect
                    # once rather than losing the utterance.
                    self.ws = None
                    if attempt == 2:
                        raise
            return "", 0

    async def close(self) -> None:
        if self.ws is not None:
            await self.ws.close()
            self.ws = None


async def sarvam_transcribe(pcm: bytes) -> tuple[str, int]:
    """POST one finished segment to Sarvam's REST STT.

    Unlike Omli and Gemini this cannot start until the segment is complete —
    it is a file upload, not a stream. Its latency therefore covers upload plus
    inference for the whole utterance and is NOT comparable to the two
    streaming numbers; it is what Sarvam REST genuinely costs.
    """
    if not SARVAM_API_KEY:
        raise RuntimeError("SARVAM_API_KEY not set on the server")
    wav = io.BytesIO()
    with wave.open(wav, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(BYTES_PER_SAMPLE)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm)

    form = aiohttp.FormData()
    form.add_field("file", wav.getvalue(), filename="utterance.wav", content_type="audio/wav")
    form.add_field("model", SARVAM_MODEL)
    form.add_field("mode", "transcribe")

    t0 = time.time()
    timeout = aiohttp.ClientTimeout(total=30)
    async with aiohttp.ClientSession(timeout=timeout) as sess:
        async with sess.post(SARVAM_URL, data=form,
                             headers={"api-subscription-key": SARVAM_API_KEY}) as resp:
            raw = await resp.text()
            if resp.status != 200:
                raise RuntimeError(f"sarvam HTTP {resp.status}: {raw[:200]}")
    return (json.loads(raw).get("transcript") or "").strip(), int((time.time() - t0) * 1000)


def omli_url(cfg: dict, session_id: str) -> str:
    q = {
        "stt_vad_backend": "kids_v5",
        "session_id": session_id,
        "confidence": cfg.get("confidence", 0.4),
        "pre_pad_ms": cfg.get("pre_pad_ms", 600),
        "post_pad_ms": cfg.get("post_pad_ms", 200),
        "min_silence_ms": cfg.get("min_silence_ms", 600),
        "hangover_ms": cfg.get("hangover_ms", 1000),
        # Pinned high on purpose. When Omli's own cap fires it truncates what
        # it emits but leaves its VAD's utterance open, and every sample between
        # the flush and the true speech-end is discarded — a whole spoken
        # sentence, measured. Our local cap below does the job losslessly,
        # because we cut from a buffer we hold rather than from Omli's stream.
        "max_segment_ms": 60000,
    }
    return f"ws://{OMLI_HOST}?" + "&".join(f"{k}={v}" for k, v in q.items())


@app.websocket("/ws")
async def ws_endpoint(client: WebSocket):
    await client.accept()
    session_id = uuid.uuid4().hex[:12]
    send_lock = asyncio.Lock()

    async def to_client(payload: dict) -> None:
        async with send_lock:
            try:
                await client.send_text(json.dumps(payload))
            except Exception:
                pass

    try:
        cfg = json.loads(await client.receive_text())
    except Exception:
        await client.close()
        return

    mode = cfg.get("mode", "both")   # omli | gemini | sarvam | both (= all three)
    want_omli = mode in ("omli", "both")
    want_gemini = mode in ("gemini", "both")
    want_sarvam = mode in ("sarvam", "both")
    pre_pad = ms_to_samples(cfg.get("pre_pad_ms", 600))
    post_pad = ms_to_samples(cfg.get("post_pad_ms", 200))
    # Our cap, applied to the local buffer. Omli's own is pinned to 60000.
    local_cap_ms = int(cfg.get("local_cap_ms", 15000))
    local_cap = ms_to_samples(local_cap_ms)
    max_seg_ms = local_cap_ms

    buf = AudioBuffer()
    omli_stt = OmliUtteranceSTT(session_id) if want_omli else None
    turn: dict | None = None                # the segment currently being spoken
    tasks: set[asyncio.Task] = set()

    def spawn(coro) -> None:
        t = asyncio.create_task(coro)
        tasks.add(t)

        def done(task: asyncio.Task) -> None:
            tasks.discard(task)
            # Without this a crash inside a spawned turn is invisible: no
            # transcript, no error, no log — the column just stays "waiting".
            if not task.cancelled() and task.exception() is not None:
                traceback.print_exception(task.exception())

        t.add_done_callback(done)

    async def deliver(label: str, pcm: bytes, start: int, end: int,
                      t: dict, continuing: bool) -> None:
        """Ship one cut to all three STTs and report it as its own row."""
        if not pcm:
            return
        dur_ms = int(len(pcm) / BYTES_PER_SAMPLE / SAMPLE_RATE * 1000)
        name = f"{session_id}_{label.replace('/', '-')}.wav"
        write_wav(SEG_DIR / name, pcm)
        await to_client({
            "type": "segment", "seq": label, "duration_ms": dur_ms,
            "start_ms": start // 16, "end_ms": end // 16,
            # "capped" now means OUR cap cut a still-running utterance, which is
            # a genuine "the child was mid-sentence" signal rather than a lost one.
            "capped": continuing,
            "wav": f"/segments/{name}",
        })

        async def omli_for() -> None:
            try:
                text, ms = await omli_stt.transcribe(pcm)
                await to_client({"type": "transcript", "source": "omli", "seq": label,
                                 "text": text, "latency_ms": ms, "at_ms": buf.total // 16})
            except Exception as exc:
                await to_client({"type": "error", "source": "omli", "seq": label,
                                 "message": str(exc)[:300]})

        async def sarvam_for() -> None:
            try:
                text, ms = await sarvam_transcribe(pcm)
                await to_client({"type": "transcript", "source": "sarvam", "seq": label,
                                 "text": text, "latency_ms": ms, "at_ms": buf.total // 16})
            except Exception as exc:
                await to_client({"type": "error", "source": "sarvam", "seq": label,
                                 "message": str(exc)[:300]})

        if omli_stt is not None:
            spawn(omli_for())
        if want_sarvam:
            spawn(sarvam_for())

        async def gemini_for() -> None:
            try:
                text, ms = await gemini_transcribe(pcm)
                await to_client({"type": "transcript", "source": "gemini", "seq": label,
                                 "text": text, "latency_ms": ms, "at_ms": buf.total // 16})
            except Exception as exc:
                await to_client({"type": "error", "source": "gemini", "seq": label,
                                 "message": str(exc)[:300]})

        if want_gemini:
            spawn(gemini_for())

    async def cut_part(t: dict) -> None:
        """Local cap reached while the child is still talking: cut and continue."""
        end = buf.total
        pcm = buf.slice(t["start"], end)
        # The first cut keeps the plain seq so it fills the row the vad_event
        # already created; only continuations get a .n suffix.
        label = t["seq"] if t["part"] == 1 else "%s.%d" % (t["seq"], t["part"])
        t["part"] += 1
        # The turn continues from here, so nothing is skipped.
        t["start"] = end
        await deliver(label, pcm, end - len(pcm) // BYTES_PER_SAMPLE, end, t, True)
        t["cutting"] = False

    async def finish_turn(t: dict) -> None:
        """Let the post-pad tail flow through, then close the turn and report.

        Runs exactly once per turn: the disconnect path also spawns this for a
        turn in flight, and a second run would report an empty transcript over
        the real one.
        """
        nonlocal turn
        if t.get("finished"):
            return
        t["finished"] = True
        while buf.total < t["cutoff"]:
            await asyncio.sleep(0.03)
        if turn is t:
            turn = None
        label = t["seq"] if t["part"] == 1 else "%s.%d" % (t["seq"], t["part"])
        await deliver(label, buf.slice(t["start"], t["cutoff"]),
                      t["start"], t["cutoff"], t, False)

    try:
        upstream = await websockets.connect(omli_url(cfg, session_id), max_size=2**24)
    except Exception as exc:
        await to_client({"type": "error", "source": "omli", "message": f"connect failed: {exc}"})
        await client.close()
        return

    async def pump_upstream() -> None:
        """Read Omli's VAD events and transcripts; open and close Gemini turns."""
        nonlocal turn
        async for raw in upstream:
            msg = json.loads(raw)
            seq = str(msg.get("chunk_seq"))
            event = msg.get("vad_event")

            if event == "start":
                # The mark lands ~one network hop late; pre_pad absorbs it.
                start = max(0, buf.total - pre_pad)
                turn = {"seq": seq, "start": start, "cutoff": None,
                        "part": 1, "cutting": False}
                await to_client({"type": "vad", "event": "start", "seq": seq,
                                 "at_ms": buf.total // 16})
                continue

            if event == "stop":
                await to_client({"type": "vad", "event": "stop", "seq": seq,
                                 "at_ms": buf.total // 16})
                if turn is not None and turn["seq"] == seq:
                    turn["cutoff"] = buf.total + post_pad
                    spawn(finish_turn(turn))
                continue

            # Transcripts from this socket are deliberately dropped: it is our
            # VAD feed only. Omli's STT is driven from deliver() instead, one
            # bounded utterance at a time.

    up_task = asyncio.create_task(pump_upstream())

    # Raw mic stream, un-segmented, so a live take can be replayed through
    # sweep.py with different VAD params: same audio, same room, only the
    # threshold changes. Four separate live takes are not comparable.
    raw_name = f"{session_id}.wav"
    raw = wave.open(str(REC_DIR / raw_name), "wb")
    raw.setnchannels(1); raw.setsampwidth(BYTES_PER_SAMPLE); raw.setframerate(SAMPLE_RATE)

    await to_client({"type": "ready", "session_id": session_id, "mode": mode,
                     "raw": f"/recordings/{raw_name}"})

    try:
        while True:
            chunk = await client.receive_bytes()
            buf.append(chunk)
            raw.writeframes(chunk)
            await upstream.send(chunk)
            # Same bytes, same moment: Gemini transcribes during the utterance
            # rather than after it, which is the only way its latency is
            # comparable to Omli's.
            # Our cap. Spawned rather than awaited: cutting involves closing and
            # reopening the Gemini window, and blocking here would stall the
            # audio still being forwarded to Omli's VAD.
            t = turn
            if (t is not None and t.get("cutoff") is None and not t["cutting"]
                    and local_cap and buf.total - t["start"] >= local_cap):
                t["cutting"] = True
                spawn(cut_part(t))
    except (WebSocketDisconnect, RuntimeError):
        pass
    except Exception as exc:
        await to_client({"type": "error", "source": "server", "message": str(exc)[:300]})
    finally:
        # A turn still open owns audio that will never arrive now; close it so
        # its transcript is reported rather than lost on disconnect.
        if turn is not None:
            turn["cutoff"] = buf.total
            spawn(finish_turn(turn))
        if tasks:
            await asyncio.wait(tasks, timeout=25)
        raw.close()
        up_task.cancel()
        await upstream.close()
        if omli_stt is not None:
            await omli_stt.close()


@app.get("/")
async def index():
    # no-store because the page is edited constantly during tuning: without it
    # the browser heuristically caches the HTML and shows stale VAD defaults
    # that no longer match what the server sends, which reads as a bug.
    return FileResponse(BASE / "static" / "index.html",
                        headers={"Cache-Control": "no-store, must-revalidate"})


app.mount("/segments", StaticFiles(directory=SEG_DIR), name="segments")
app.mount("/recordings", StaticFiles(directory=REC_DIR), name="recordings")
