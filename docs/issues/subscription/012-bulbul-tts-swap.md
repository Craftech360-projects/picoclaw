---
id: SUB-12
title: "Bulbul v3 TTS swap + Hinglish listening A/B"
type: HITL
status: closed
triage: needs-human
blocked-by: []
---

## Parent

`docs/cheeko-pricing-strategy.md` §6 dependency 1 (the ₹2.7 → ₹1.5/min move); spec §8 phase 0. **Without this swap every plan loses money.**

## What to build

Add a Sarvam Bulbul v3 TTS builder to the picoclaw worker's provider set (the repo already has `sarvam_tts` for reference), configurable from the manager provider tables like the existing TTS providers, at 24kHz output matching the pipeline. Then the HITL part: an A/B listening comparison against the current ElevenLabs mv2 on real kids' Hinglish/code-mixed prompts — the human judges quality and makes the swap call. If Bulbul fails the ear test, fall back to ElevenLabs Flash as the cost mitigation.

## Acceptance criteria

- [ ] Bulbul v3 selectable as active TTS via the manager provider config; live session produces correct 24kHz audio
- [ ] A/B sample set generated (same scripts, both voices) for the listening session
- [ ] Human verdict recorded here (swap / fallback) with notes
- [ ] If swap: production provider flipped and per-minute cost re-measured against the ₹1.5/min planning basis
- [ ] Barge-in and chunked-TTS pacing behave correctly with the new provider

## Blocked by

None — can start immediately (human needed for the listening verdict).

## Resolution

Closed 2026-07-22 per Rahul — work completed outside the ticket flow. Listening
verdict / cost re-measure details not recorded here; add notes if needed.
