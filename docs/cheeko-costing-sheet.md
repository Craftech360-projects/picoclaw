# Cheeko Voice-Agent Costing Sheet

> For subscription-plan design. Unit prices verified against official pricing pages on **2026-07-10**
> (deep-research run, 23/25 claims confirmed 3-0). Consumption ratios measured from production DB
> (`device_token_usage`, `voice_session_messages`: 29 usage rows, 658 session-minutes, avg session 22.7 min).
> FX assumption: **$1 = ₹85**.

## 1. Measured consumption (your real data, not industry averages)

| Metric | Value | Source |
|---|---|---|
| LLM input tokens / session-min | **~14,950** | full history + persona re-sent every turn |
| LLM output tokens / session-min | ~58 | replies avg 124 chars |
| Turns (messages) / min | 1.65 | |
| TTS characters / session-min | ~145 | agent speaks little per minute of session |
| STT audio streamed | ≈ full session duration | pipeline streams the track continuously |
| Avg session length | 22.7 min | |

⚠️ The 15k input-tok/min is the outlier vs industry (~1–3k/min typical). Cost lever #1 (see §5).

## 2. Verified unit prices — ACTIVE stack

| Stage | Provider / model | Price (PAYG) | Growth/volume | Source |
|---|---|---|---|---|
| STT | Deepgram **nova-2** | ⚠️ off public price sheet (legacy; historic ~$0.0059/min — confirm in console) | — | deepgram.com/pricing |
| STT | Deepgram **nova-3** (migration target) | $0.0048/min mono streaming | $0.0042/min | deepgram.com/pricing |
| TTS | Deepgram **aura-2** | $0.030 / 1k chars | $0.027 / 1k chars | deepgram.com/pricing |
| LLM | OpenRouter **google/gemma-4-31b-it** | $0.12 in / $0.35 out per 1M tok (cheapest endpoint; pin provider or routed cost can hit $0.39/$0.97) | :free variant 20 RPM/200 RPD | openrouter.ai/google/gemma-4-31b-it |
| Moderation | OpenAI **omni-moderation-latest** | **$0 (free)** — rate-limited 250–5,000 RPM by tier | Tier 5 cap ≈ 5,000 RPM: constraint at ~10k+ concurrent devices | openai docs |
| Image | HF → **FLUX.1-schnell** | passthrough, ~$0.003/image (fal $0.003/MP) | — | huggingface.co/docs/inference-providers/pricing |
| Infra | **LiveKit Cloud** | Plans $0 / $50 / $500 /mo. Agent-session $0.01/min **only if agent runs on LiveKit compute — yours is self-hosted (pm2), so N/A**. Participant minutes $0.0005–0.0004/min after 5k/150k/1.5M included, + bandwidth $/GB | Scale tier ~15–25% inference discounts | livekit.com/pricing |

Notes: Deepgram STT bills on audio *sent*, not connection time — an idle websocket costs $0. Growth tier needs ~$4k+/yr prepay.

## 3. Per-minute cost — active stack (measured ratios)

| Line item | Math | $/session-min |
|---|---|---|
| STT (nova-3 PAYG; nova-2 similar or worse) | 1 min audio × $0.0048 | $0.0048 |
| TTS (aura-2) | 145 chars × $0.030/1k | $0.0044 |
| LLM input | 14,950 tok × $0.12/1M | $0.0018 |
| LLM output | 58 tok × $0.35/1M | $0.00002 |
| Moderation | free | $0 |
| LiveKit (Ship plan, 2 participants/room: gateway + agent) | 2 × $0.0005 | $0.0010 |
| **Total marginal** | | **≈ $0.012/min ≈ ₹1.0/min** |

Excluded: LiveKit plan fee ($50–500/mo fixed), bandwidth, your own server/GPU, Supabase, S3/CloudFront, EMQX hosting, Cerebrium music/story bots, Mem0/Qdrant. Imagine feature: ~$0.003/image, count per use not per minute.

## 4. Monthly cost per device (marginal, active stack)

| Usage | Min/month | USD/device-mo | INR/device-mo |
|---|---|---|---|
| Light — 15 min/day | 450 | $5.40 | ₹460 |
| Medium — 30 min/day | 900 | $10.80 | ₹920 |
| Heavy — 60 min/day | 1,800 | $21.60 | ₹1,840 |

Fleet examples (medium usage): 1,000 devices ≈ $10.8k/mo; 10,000 ≈ $108k/mo — before volume discounts.
Subscription implication: **price tiers must cap daily minutes** — an uncapped heavy user costs ~₹1,800+/mo in API spend alone.

## 5. Cost levers, biggest first

1. **LLM context bloat** — 15k input-tok/min is ~5–10× typical. Trim history (rolling window + your existing session summaries), shrink AGENT.md/persona, and the LLM line drops from $0.0018 to ~$0.0003/min. More importantly it protects you if you ever switch to a pricier model: at GPT-class prices ($1–3/1M in) this line becomes $0.015–0.045/min and dominates everything.
2. **STT streams full session** — you pay for silence between turns. Gating audio to STT on VAD activity (you already run TEN VAD) could cut STT ~40–60% → saves up to ~$0.003/min.
3. **Deepgram Growth tier** — ~12–16% off STT+TTS for $4k+/yr commit; worth it above ~1,000 medium-usage devices.
4. **Nova-2 → Nova-3** — nova-2 is off the public price sheet (legacy risk); nova-3 is cheaper ($0.0048 vs ~$0.0059) and current. Config change in `stt_providers`.
5. **TTS is already cheap** — don't bother optimizing; switching to ElevenLabs multilingual v2 would 3.3× this line ($0.10/1k chars), so keep aura-2 unless voice quality demands otherwise.

## 6. Verified alternatives (per-unit, same units)

| Stage | Provider | Verified price | vs active |
|---|---|---|---|
| STT | ElevenLabs Scribe v2 Realtime | $0.39/hr = $0.0065/min | +35% vs nova-3 |
| STT | ElevenLabs Scribe batch | $0.22/hr = $0.0037/min | batch only, not streaming |
| STT | Cartesia Ink-2 (supersedes configured `ink-whisper`, 3× credit burn) | ~$0.0071/min (Startup $49 plan credits) | +48% |
| STT | Cartesia legacy ink-whisper | ~$0.0024/min | −50%, legacy-model risk |
| STT | Sarvam (STT generic; Saaras v3 not itemized) | **₹30/hr = ₹0.50/min ($0.006/min)** | +25%, but INR-billed + Indic languages |
| TTS | ElevenLabs multilingual v2 | $0.10/1k chars | 3.3× aura-2 |
| TTS | ElevenLabs Flash/Turbo | $0.05/1k chars | 1.7× aura-2 |
| LLM | OpenRouter gemma-4-31b-it:free | $0, 20 RPM / 200 RPD | dev/testing only |

Not verified this run (re-check before relying on): AssemblyAI, Groq Whisper/Qwen3-32B, OpenAI whisper-1, Google Chirp 3, Azure/AWS STT, Soniox, Speechmatics, Gladia, Voxtral, GPT-5-nano, Runware per-image, Kokoro-82M self-host GPU cost, LiveKit bandwidth $/GB.

## 7. Open questions

- Confirm in the Deepgram console what nova-2 actually bills today; migrate to nova-3 regardless.
- Is LiveKit self-hosted or Cloud? (CI deploys the *worker* to your server; if the LiveKit *server* is Cloud, participant minutes + plan fee apply as modeled; if fully self-hosted, LiveKit line → $0 + server cost.)
- OpenAI moderation Tier-5 5,000 RPM ceiling: at ~1.65 msgs/min/device, ~3,000 concurrent devices saturate it — ask OpenAI for a raise or add a fallback moderator before that scale.
- OpenRouter routing: pin the $0.12/$0.35 endpoint (`provider.order`) or budget for routed-price variance up to 3×.
