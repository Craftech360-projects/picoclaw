---
id: 9
title: Kid-facing lapse experience
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

When the gateway refuses an AI session (no active plan / bucket exhausted / daily cap hit), what exactly does the child experience?

1. **Capability check first** (codebase): what can the gateway send a device outside a LiveKit session — pre-recorded audio URL over MQTT? A `settings_update`-style prompt? Does firmware have local canned audio? (See `gateway/mqtt-gateway.js` hello fast-path and the Phase 2 note "TTS-less notice or pre-recorded prompt".)
2. **Message content per gate reason**: trial-ended vs monthly-bucket-empty vs daily-cap-hit should feel different ("ask Mama to top up" vs "Cheeko needs a nap till tomorrow").
3. **Languages**: notice must match the device's configured language (11 Indian languages via Sarvam) — pre-recorded per language, or one-time generated?
4. Parent-side mirror: what push notification fires when a kid hits a gate.

## Resolution (2026-07-14, capability check + grilling)

1. **Capability confirmed (codebase)**: the gateway already sends `tts` MQTT messages with a `text` field outside LiveKit (`mqtt-gateway.js` ~:1843), and it owns the UDP audio socket — the hello fast-path establishes UDP *before* LiveKit exists. **Mechanism: gateway streams a pre-recorded Opus clip over the existing UDP session + `tts start`/`stop` signals. No LiveKit room, no per-use TTS cost**; works even when LiveKit is degraded.
2. **One generic message** for all gate reasons (user's call, over the reason-specific recommendation): "ask Mumma/Papa to check the Cheeko app" — never mentions money to the child. ⚠️ Flagged tradeoff: a daily-cap kid triggers "ask parents" when nothing needs paying; upgrade path is cheap (add reason-specific clips later) since the delivery mechanism is clip-agnostic.
3. **Languages**: **English-only clip at launch (1 clip)** — amended by user 2026-07-14 from the earlier Hindi+English pick; all device languages fall back to English until device-language distribution justifies recording more. Clip generated once at build time (e.g. Bulbul).
4. **Parent push mirror: plan-related gates only** — trial-ended, plan-lapsed, monthly-bucket-empty (actionable). NO push on daily-cap hits (healthy usage; would fire most evenings) — those show passively in the app's usage view.
