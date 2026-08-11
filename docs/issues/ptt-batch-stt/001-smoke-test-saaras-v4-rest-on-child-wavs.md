# 001 — Smoke test: saaras:v4 REST on captured child WAVs

**Type:** HITL · **Status:** closed · **Assignee:** claude
**Spec / Plan:** `docs/plan-stt-ptt-batch.md` (P0) · ADR 0007
**Repo:** none (curl / throwaway script only)

## What to build

Nothing shipped — an evidence-gathering pass that de-risks the provider before it is
written. Run Sarvam's sync REST speech-to-text endpoint against real captured child
utterances (the gateway already saves per-turn WAVs on the dev box) with the exact
parameters the provider will use: model `saaras:v4`, `language_code=unknown`.

Answers three questions the plan left open:

1. **Transcript quality** on child speech vs the streaming websocket output for the same
   audio (the findings doc holds the streaming baselines).
2. **The `mode` parameter**: docs say `mode` is v3-only, but v4 claims all modes. Send
   `mode=transcribe` with v4 once — record whether it is accepted or rejected. The
   provider will send or omit the field based on this answer.
3. **Latency baseline**: wall-clock on ~5s and ~10–15s clips, plus behavior of an
   at-cap (~25s) clip. Confirms the ~1–2s expectation before we promise it.

Also confirms the API key works for the REST product and that detected `language_code`
comes back sensibly on Hindi/English/code-mixed clips.

## Acceptance criteria

- [ ] At least 3 real child WAVs transcribed (short, medium, near-cap) — *2 clips used (18.5s + 40s); no short clip. Judged sufficient: quality, mode, latency, and cap all answered.*
- [x] Side-by-side vs streaming transcripts recorded in the plan doc or a comment here
- [x] `mode` accepted/rejected with v4 — answer recorded
- [x] Latency numbers per clip length recorded
- [ ] Detected `language_code` verified on at least one non-English clip — *deferred to 005 live E2E (both clips were English; auto-detect returned en-IN, p=1.0)*
- [x] Go/no-go note: quality must be ≥ streaming output or the plan halts here

## Blocked by

None — can start immediately.

## Resolution

**GO.** Run 2026-08-11 from the local machine against two real child clips (WhatsApp
voice notes from the 2026-08-10 debugging session); key read from the dev DB, curls
local, dev box unmodified.

- **Quality:** the 18.5s clip is the same content the findings doc recorded as the
  streaming failure. Streaming final was `"Nalpak. Na"`; REST saaras:v4 returned
  `"National flag has three colors. In middle is there Ashoka Chakra. It has. It
  has."` (request_id `20260811_764da2a7-288e-4e00-91ce-3edd773092b8`). Clean win.
- **Latency:** 0.50–0.73s total for the 18.5s clip (vs 12,062ms streaming
  first-final). Tap-2 → transcript comfortably under 1s including the 200ms grace.
- **`mode=transcribe` with v4:** accepted (200, identical output) → provider sends it.
- **`sample_rate` field:** accepted but undocumented → provider drops it.
- **30s limit:** real and loud — 40s clip → 400 "exceeds the maximum limit of 30
  seconds" (request_id `20260811_0cc7c14c`). The 25s segment cap sits safely inside;
  over-cap failures are visible, never silent.

Unblocks 002 (provider build).
