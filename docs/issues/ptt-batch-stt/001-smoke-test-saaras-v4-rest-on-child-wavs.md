# 001 — Smoke test: saaras:v4 REST on captured child WAVs

**Type:** HITL · **Status:** open
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

- [ ] At least 3 real child WAVs transcribed (short, medium, near-cap)
- [ ] Side-by-side vs streaming transcripts recorded in the plan doc or a comment here
- [ ] `mode` accepted/rejected with v4 — answer recorded
- [ ] Latency numbers per clip length recorded
- [ ] Detected `language_code` verified on at least one non-English clip
- [ ] Go/no-go note: quality must be ≥ streaming output or the plan halts here

## Blocked by

None — can start immediately.
