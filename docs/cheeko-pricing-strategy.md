# Cheeko — Pricing Strategy (question-based)

> Created 2026-07-13. Cost basis: `docs/cheeko-costing-sheet.xlsx` (voice) and
> `D:\line_art\image_provider_comparison_v2.xlsx` (imagine).
> Two views below: **customer-facing = questions**, **internal = minutes + COGS**.
> Enforcement lives in `docs/plan-usage-tracking-and-limits.md` (Phase 2 = per-device caps).

---

## 1. The stack these prices assume

| Stage | Choice | Why this one |
|---|---|---|
| **STT** | **Sarvam (Saarika/Saaras)** — keep | Regional-language coverage (11 Indian langs). Do **not** swap to Voxtral — it only does Hindi/English, and STT is the cheapest stage anyway (~₹0.57/min). |
| **LLM** | **gpt-4.1-mini, context-trimmed** (upgrade path: gemma-4-31b-it after Hinglish A/B) | History summarization is already live (`agent_bridge.go:1667`, summarize at 20 msgs, keep last 4). Remaining input cost is system-prompt overhead, attacked by voice-directive compression + tool-allowlist pruning + prompt caching. |
| **TTS** | **Bulbul v3** (swap from ElevenLabs mv2) | **The money lever.** TTS is ~65% of current per-minute cost. Bulbul v3 is 3.2× cheaper than mv2 *and* better Hinglish fit. Beta pricing → validate before GA. |
| **Image** | **Runware FLUX.2 klein 4B** + Groq-turbo STT + Groq moderation | ~₹0.10/image all-in. HuggingFace as last-resort fallback (already coded). |
| **Moderation** | Groq llama-3.1-8b-instant (+ free OpenAI omni as 2nd layer) | Near-free, mandatory on every path. |

### Per-component cost (₹ per session-minute, measured usage: 153 TTS chars/min, ~14.3k in-tok/min)

| Component | ₹/min | Notes |
|---|---|---|
| Sarvam STT | 0.57 | per audio-minute, INR-billed |
| gpt-4.1-mini (trimmed) | 0.12 | after history summarization |
| **Bulbul v3 TTS** | **0.46** | @153 chars/min; biggest single line |
| LiveKit (2 participants) | 0.10 | worker self-hosted, no agent-compute fee |
| Moderation | 0.00 | free |
| **Voice total** | **~₹1.25/min** | committed planning basis rounded up to **₹1.5/min** (buffer for Bulbul beta price; falls back to EL Flash if A/B fails) |

**Current stack (ElevenLabs mv2) is ~₹2.7/min** — every price below assumes the Bulbul swap has shipped.

### Cheaper target stack (needs quality A/B before committing)

| Swap | ₹/min | Risk |
|---|---|---|
| gemma-4-31b-it instead of gpt-4.1-mini | ~₹1.0/min | Hinglish quality unbenchmarked; **pin provider** on OpenRouter; use paid route, **not** `:free` |
| Bulbul v2 instead of v3 | even cheaper | weaker Hinglish/code-mixing |

Plan against **₹1.5/min committed**; treat ₹1.0 as upside you pocket if the A/B passes.

---

## 2. Cost per unit (what one question / image costs)

- Measured **1.6 questions/min** → 1 question ≈ **0.63 min**
- **Cost per question ≈ ₹0.90** (committed ₹1.5/min) → **₹0.60** on the target stack
- **Cost per image ≈ ₹0.10** all-in
- 1 question = one child ask + Cheeko reply (one exchange). Backend counts **user messages** — already recorded in `device_token_usage_session.message_count`, so questions are directly meterable.

---

## 3. Plans — customer view (questions)

| | **Starter — ₹199/mo** | **Family — ₹499/mo** ⭐ | **Premium — ₹999/mo** |
|---|---|---|---|
| **Questions** | **100/mo** | **300/mo** | **800/mo** |
| Fair-use cap | 15/day | 40/day | 80/day |
| **AI images** | 150/mo | Unlimited (25/day) | Unlimited |
| Characters / RFID | all | all + memory | all + 2 kid profiles |
| Parent app | usage view | weekly summary | deep insights + priority |

- **Annual = pay 10 months** (2 free).
- **Family ₹499 / 300 questions is the hero** — ~10 questions/day, the daily-use sweet spot. Starter anchors cheap (decoy); Premium keeps ₹499 from looking like the ceiling.
- Bundle images generously — they cost ~₹0.10, so "unlimited" is nearly free perceived value.

---

## 4. Same plans — internal view (minutes + COGS)

Cost/question ₹0.90 committed. Avg-use assumes ~50% bucket consumption (validate against real usage).

| Plan | Price | Questions | ≈ minutes | Full-usage COGS | Avg COGS @50% | Gross margin (avg) |
|---|---|---|---|---|---|---|
| Starter | ₹199 | 100 | ~63 min | ₹90 (45%) | **~₹48 (24%)** | ~76% |
| Family ⭐ | ₹499 | 300 | ~190 min | ₹270 (54%) | **~₹145 (29%)** | ~71% |
| Premium | ₹999 | 800 | ~500 min | ₹720 (72%) | **~₹300 (30%)** | ~70% |

Gross margin (avg) = price − avg COGS. Still has to cover: LiveKit plan fee, servers, S3/CDN, Supabase, EMQX, payment-gateway (~2%), support, CAC, profit.

Full-usage COGS is the worst case (every question used). It's bounded by the **daily fair-use cap** — that's what stops the heavy 10% from erasing margin.

---

## 4b. Alternative packaging — minute-based

Same economics, "talk time" instead of "questions" (÷1.6 q/min). Use this if research
shows parents prefer a time bucket. Minutes are the native backend meter (Phase 2 enforces
minutes directly), so this is the simpler enforcement — questions are a relabel on top.

**Customer view:**

| | Starter — ₹199/mo | Family — ₹499/mo ⭐ | Premium — ₹999/mo |
|---|---|---|---|
| **Talk time** | **60 min/mo** (~8 min/day) | **200 min/mo** (~15 min/day) | **500 min/mo** (~30 min/day) |
| *≈ questions* | ~40 | ~130 | ~330 |
| **AI images** | 150/mo | Unlimited (25/day) | Unlimited |
| Characters / RFID | all | all + memory | all + 2 kid profiles |
| Parent app | usage view | weekly summary | deep insights |

**Internal view** (cost ₹1.5/min committed, ~50% avg use):

| Plan | Price | Minutes | Full-usage COGS | Avg COGS @50% | Gross margin (avg) |
|---|---|---|---|---|---|
| Starter | ₹199 | 60 | ₹90 (45%) | ~₹48 (24%) | ~76% |
| Family ⭐ | ₹499 | 200 | ₹300 (60%) | ~₹150 (30%) | ~70% |
| Premium | ₹999 | 500 | ₹750 (75%) | ~₹300 (30%) | ~70% |

**Question vs minute — which to ship:**

| | Questions | Minutes |
|---|---|---|
| Parent comprehension | "ask 10 things/day" — concrete | "talk 15 min/day" — feels like phone bill |
| Enforcement | count user messages (also tracked) | native meter (Phase 2) — simplest |
| Abuse risk | one question can ramble → needs minute cap under it | self-bounding |
| Recommendation | **customer-facing** | **cost backstop + fallback framing** |

Ship **questions to the customer, enforce minutes underneath.** Best of both.

---

## 5. Guardrails (why question-billing is safe here)

1. **Daily *minute* safety cap underneath the question quota.** A question quota alone is gameable — one "question" that spawns a 10-min story costs 16× a normal one. Parent sees questions; backend (Phase 2) enforces both a monthly question bucket **and** a daily minute cap. The minute cap bounds the worst day.
2. **Count every exchange as 1 question** — don't try to detect "same-topic follow-ups" (too fuzzy). Size buckets generously instead.
3. **Breakage funds the margin.** Most kids use ~50% of their bucket; the daily cap catches the outliers. Recheck the 50% assumption after one month of `device_token_usage_session` data.

---

## 6. Dependencies (before launch)

1. **Swap TTS → Bulbul v3** (the ₹2.7 → ₹1.5/min move). Without it, every plan loses money.
2. **Fix tracking hole 1.3** (`agent_bridge.go:1737` `bridgeSummarizeBatch` has no `recordUsage`) — summarization spend is paid but uncounted, undercounting the bill.
3. **Land Phase 2 enforcement** — the daily minute cap + `GET /device/:mac/usage-today`, which every price here relies on.

Context-trim (LLM history summarization) is **already shipped** — not a blocker.

---

## 7. Open validations

- [ ] Bulbul v3 Hinglish listening A/B on real kids' speech (and price at GA).
- [ ] If chasing ₹1.0/min: gemma-4-31b-it quality A/B vs gpt-4.1-mini.
- [ ] Confirm ~50% average bucket consumption once live.
- [ ] Van Westendorp price-sensitivity check on ₹199 / ₹499 / ₹999 with Indian parents.
