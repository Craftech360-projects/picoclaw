# Omli VAD hands-free POC — Results

**Date:** 2026-09-07
**Question:** the lab rig turns a 3m44s child conversation into ~39 VAD segments. If a
segment were a turn that would be 39 LLM calls and 39 spoken replies. Does LiveKit's
`AgentSession` plus the turn detector collapse them?
**Short answer:** No. It collapses the *replies*, not the *turns*. Every configuration
fired the LLM ~26 times and spoke 6–13 times, because each new turn cancels the reply
still being spoken.

Rig: the `omli-poc` worker on the dev box (`64.227.170.31`). Pipeline is Omli `kids_v6`
VAD → STT (`POC_STT` selects `omli` / `gemini` / `sarvam`) → `gemini-2.5-flash` →
Sarvam TTS, with `inference.TurnDetector(version="v1-mini")` deciding turn ends.
Evidence is `/root/omli-poc/turns/<room>.jsonl`, one line per session event.

---

## How this was run

`poc_session.py` in `omli-poc` creates a room, dispatches the worker, joins as an
ordinary participant and plays a WAV through a microphone track at 1x, with **no
push-to-talk signalling of any kind** — the VAD and the turn detector are the only
things deciding where turns start and stop. It records; it asserts nothing.

The audio is `/root/sttlab/recordings/c06d58a80bf2.wav` — 3m44s, an adult questioning
a 4-year-old, full of one-word answers. It is the same file every VAD measurement in
[`omli-vad-v6-results.md`](omli-vad-v6-results.md) used, so the segment counts below can
be read against the 37–39 the lab rig found.

One run per STT, the recording played end to end each time (measured pacing 1.000x),
then 30s of silence so the last turn could finish. The agent's greeting plays first and
is excluded from the reply counts where noted.

---

## The numbers

| | Omli STT | Gemini STT | Sarvam STT |
|---|---|---|---|
| VAD speech events (`user_state` → speaking) | 29 | 32 | 33 |
| Segments that produced a transcript (`user` lines) | 25 | 26 | 26 |
| — of those, empty | **0** | **0** | **0** |
| **Final user turns handed to the LLM** | **25** | **24** | **26** |
| Turns assembled from more than one segment | 0 | 2 | 0 |
| LLM invocations (`agent_state` → thinking) | 26 | 26 | 26 |
| Replies the agent began speaking | 15 | 15 | 8 |
| Replies that completed | 10 | 14 | 7 |
| **Replies to the recording (greeting excluded)** | **9** | **13** | **6** |
| Completed replies ending mid-sentence | 2 | 6 | 2 |

Segments → turns is **1:1**. Across all three runs the turn detector merged two
segments into one turn exactly twice, both in the Gemini run (`"I am four years."` +
`"Do you like ice cream?"`, and `"Pandit ji."` + `"12345678910"`). Everywhere else the
user item committed to the LLM is byte-for-byte the single segment that preceded it.

So the answer to the question this POC exists for: **`AgentSession` + `v1-mini` did not
collapse segments into turns.** A four-minute conversation cost 26 LLM calls in every
configuration — the same order as the 39 the raw segment count predicted, not a
fraction of it.

What *does* get collapsed is speech. The agent tried to speak 15/15/8 times and finished
10/14/7, because a new user turn arriving mid-reply preempts the one in flight. The
conversation is quieter than 26 turns would suggest, but the cost and the latency are not.

### One-word answers

| | Omli | Gemini | Sarvam |
|---|---|---|---|
| User turns of one or two words | 7 | 11 | 10 |
| …that got their own reply before the next turn | 5 | 4 | 3 |

They do get their own turn — `"Orange."`, `"Water."`, `"Yes."`, `"No."`, `"Hello."`,
`"Blue."` each became a separate user item and a separate LLM call. Whether a reply is
*heard* is a race: about half the time the next question from the recording arrived
first and killed it.

### Replies to silence or noise

None. Every run produced zero empty transcripts, and no assistant item appears without a
non-empty user turn ahead of it. The VAD fired 4–7 more speech events than it produced
transcripts (29 vs 25, 32 vs 26, 33 vs 26), so some segments did yield nothing — and
correctly produced no reply.

### False turn ends, and falling behind

Turn commit is usually prompt but the tail is bad:

| segment → turn-commit lag | Omli | Gemini | Sarvam |
|---|---|---|---|
| median | 0.79 s | 0.62 s | 2.04 s |
| max | 11.43 s | 8.43 s | 11.45 s |

The maximum is not a one-off. In the Omli run, four consecutive turns between t=103 and
t=148 each committed ~11.3 s after their transcript arrived: the pipeline queued up
behind an in-flight LLM call and the agent stayed roughly eleven seconds behind the
conversation until the recording gave it a gap.

Mid-sentence turn ends are visible but hard to attribute from transcripts alone.
Adjacent fragments were committed as separate complete turns about a second apart —
Omli t=87.1 `"वाटर यूज़"` then t=88.2 `"ये और ये देखो।"`; Gemini t=128.0 `"Finger."` then
t=131.2 `"Pandit ji."`. Whether those are one interrupted sentence or two real utterances
cannot be settled without listening to the audio, so this is reported as observed, not
as a defect count.

### One run failed, and failed silently

The first Sarvam attempt (`turns/poc-sarvam-96c251.jsonl`) produced **3 transcripts in
3m44s** while the VAD went on firing 36 times. A single `asyncio.TimeoutError` opening
the connection to the Sarvam endpoint propagated out of `_recognize_impl` and killed
LiveKit's `_stt_pump` for the remainder of the session. Nothing recovered it, nothing
restarted, and from the room's point of view the agent simply stopped hearing anybody
while continuing to answer the last thing it had. The retry (`poc-sarvam-859d80.jsonl`,
the run in the tables) had no such error, so the timeout is intermittent — but the
failure mode is not: **one transient STT error ends recognition for the whole call.**

An earlier Omli run (`turns/poc-omli-371347.jsonl`, 23 s of log) was cut short when the
pm2-managed worker was restarted mid-session. It is not counted anywhere above. The three
reported runs used a worker started outside pm2 and each was verified still alive after
its recording finished.

### Against the lab rig

The lab rig found 37–39 VAD segments in this file. Through a LiveKit room the agent's own
VAD reported 29–33 speech events and 25–26 transcribed segments. Same order, consistently
fewer. Two differences could account for it and neither was isolated here: the room path
Opus-encodes and decodes the audio where the rig reads the WAV directly, and `user_state`
collapses back-to-back segments into a single speaking span. Segment arrival rate was not
suppressed while the agent was talking (segments arrived at 14.2/min while speaking vs
4.4/min while listening in the Omli run), so the agent's own speech is not eating them.

---

## What this does not tell you

- **This was a recording replayed into the room, not a child at a microphone.** A WAV was
  published as a participant track at 1x. Nobody spoke live.
- **Barge-in is only half tested.** The recording plays continuously, so it did interrupt
  the agent's replies and those interruptions are real (15 speech attempts, 10 completions
  in the Omli run). What is untested is a child *choosing* to interrupt — reacting to what
  the toy just said, at the moment it says it, at whatever volume a startled four-year-old
  uses.
- **The child in the recording is answering an adult, not the agent.** The turn structure
  is an adult interviewer's, not a conversation with Cheeko. It also means the room
  contains **two speakers** and the agent treats both as one user, so some of the churn
  above belongs to the recording, not to a toy alone with one child.
- **No microphone, no network conditions.** No `getUserMedia`, no browser capture, no
  packet loss, no room acoustics, no distance from the mic — a clean 16 kHz file went in.
- **No echo, and the browser would have hidden it anyway.** Nothing played back into the
  input during any run. Even if these sessions had been driven from a real browser tab,
  Chrome's WebRTC echo cancellation would have removed the agent's own voice from the
  captured audio before the VAD ever saw it. The toy has no such thing. **Hands-free on
  the actual device — where the mic hears the speaker — remains completely unproven by
  this test.**
- **One recording, one run per STT.** No repeats, so none of these counts carry an error
  bar, and a second run of the same configuration would not land on the same numbers.

---

## Recommendation

Do not ship hands-free on this stack as it stands: the turn detector leaves segments and
turns at 1:1, so a four-minute conversation costs ~26 LLM calls and leaves the agent up to
eleven seconds behind while most of its replies are cut off mid-sentence. Either add turn
assembly above the VAD — hold segments until the child has actually finished, rather than
treating every speech end as a turn end — or keep push-to-talk, and in both cases test on
the toy's own microphone, where echo cancellation will not be doing the work for us.
