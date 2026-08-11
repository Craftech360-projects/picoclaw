# 005 — Dev deploy, manager flip, live end-to-end verification

**Type:** HITL · **Status:** open
**Spec / Plan:** `docs/plan-stt-ptt-batch.md` (P3 + verify table) · ADR 0007
**Repo:** both (dev box `64.227.170.31` only — never prod/EKS)

## What to build

Nothing new — prove the whole path live. Deploy the picoclaw branch to the dev box,
flip the manager's active STT provider to `sarvam_rest` (model `saaras:v4`, language
`unknown`, existing Sarvam key), and run client.py sessions covering every semantic
from the plan:

1. **Happy turn:** talk, press `s` → spoken reply; transcript correct and in the
   spoken language; first-final latency logged and compared against the 12062ms
   streaming baseline.
2. **Empty tap:** press `s` without speaking → "I didn't hear you!" prompt, session
   continues, no LLM dispatch.
3. **Multi-turn:** several consecutive turns — no audio bleed between utterances.
4. **Rollback drill:** flip provider back to `sarvam` → streaming+VAD behavior
   returns without a redeploy.

Record the numbers in the plan doc. This gate decides whether the firmware phase
(real Talk-card device, then Cancel Turn via double-click) begins.

## Acceptance criteria

- [ ] Worker built and running the branch on the dev box (pm2, existing recipe)
- [ ] Manager flip activates `sarvam_rest` without worker restart (TTL pickup)
- [ ] Scenarios 1–4 pass; latency + language observations recorded
- [ ] No regression for a normal streaming session after rollback flip
- [ ] Go/no-go note for the firmware phase

## Blocked by

- 002, 003 (picoclaw side) · 004 (simulator side)
