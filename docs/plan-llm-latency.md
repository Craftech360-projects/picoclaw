# Plan — LLM latency on the Cheeko voice path

Written 2026-08-07, from the diagnosis session that followed
`handoff-llm-latency-2026-08-06.md`. Method: `diagnosing-bugs`, performance branch —
baseline measurement first, bisect second, fix third.

**Directive from Rahul: latency wins over cost. Do not trade response speed for price.**
This inverts the costing-sheet's framing — routing decisions below optimize for
time-to-first-token, and the price consequences are accepted.

## Goal

Greeting `llm_first_token_ms` reliably under ~2,000 ms (currently 14,187 ms measured),
with the fix verified by a repeatable measurement loop, not a single lucky run.

Non-goals: model quality changes (Hinglish A/B is a separate decision), the manager-API
side, ticket 005 (production promotion — HITL, human-run only).

## The measurement that started this

Real Riddler session, 2026-08-06 ~15:47 IST, from the worker's own turn latency summary:

| Marker | Greeting (turn 1) | User turn (turn 2) |
|---|---|---|
| `stt_first_final_ms` | — (no STT) | 1,880 |
| `llm_first_token_ms` | **14,187** | 2,059 |
| `llm_final_token_ms` | **37,276** | 6,472 |
| `tts_first_audio_ms` | **26,342** | 2,787 |
| `turn_total_e2e_ms` | **42,475** | 19,988 |

Request config: `model_id=google/gemma-4-31b-it:deepinfra`, `messages=13`,
`max_tokens=420`, `tools=7`, `streaming=true`. The worker logged
`openai_compat stream: routed to upstream provider "CoreWeave"`.

The greeting is the headline. Turn 2 is unremarkable by comparison.

## Evidence gathered so far (2026-08-07, code inspection only — nothing measured yet)

1. **Routing is the prime suspect, and the codebase already knew.**
   `pkg/providers/openai_compat/provider.go:115` documents measurements against this
   exact prompt: DeepInfra warm 717 ms / worst 1,486 ms; Parasail warm 1,552 ms with a
   **52,130 ms outlier** in six requests. The comment states OpenRouter's default
   routing weights price and "will hand a child that 52s wait sooner or later."

2. **The existing pin is inert unless an env var is set.**
   `openRouterProviderOrder()` (`provider.go:518`) reads `OPENROUTER_PROVIDER_ORDER`
   from the environment; unset → no `provider` block is sent → OpenRouter falls back to
   price-weighted load balancing. `docs/issues/quiz-bank/008-production-promotion.md:79`
   warns verbatim: "Set `OPENROUTER_PROVIDER_ORDER` in the pod env or the provider pin
   is inert." The k8s deployment sets it (`deploy/k8s/livekit-deployment.yaml:72`); the
   local worker that produced the measurement evidently did not — CoreWeave served the
   request despite the `:deepinfra` suffix in the model slug.

3. **The `:deepinfra` slug suffix did not hold.** Whether OpenRouter honors a provider
   suffix on this slug is unverified; empirically the request went to CoreWeave. Under
   an explicit `provider` block this becomes moot, but confirm during measurement.

4. **Prompt caching is confirmed dead on this stack.** `supportsPromptCacheKey()`
   (`provider.go:590`) sends `prompt_cache_key` only to `api.openai.com` / Azure —
   deliberate, other providers 422 on unknown fields. The only caching available is the
   **upstream's own prefix KV cache**, which each upstream keeps separately — so
   provider-bouncing also destroys caching. Pinning is therefore both the routing fix
   and the caching fix.

5. **OpenRouter's levers** (docs/guides/routing/provider-selection, fetched 2026-08-07):
   - Default: price-based load balancing, inverse-square weighted — cheap providers get
     exponentially more traffic.
   - `provider.sort: "latency" | "throughput" | "price"` — sorting disables load
     balancing entirely.
   - `preferred_max_latency` — deprioritizes providers over a latency threshold,
     supports percentile cutoffs (p50/p75/p90/p99).
   - `provider.order` (sequential pin), `provider.only`, `provider.ignore`,
     `allow_fallbacks` (default true).
   - `:nitro` slug suffix = shorthand for throughput sorting.

6. **Config-only test path exists.** `extra_body` on the model's provider config
   (`pkg/config/config.go:978`) is merged into the request body **last**
   (`provider.go:173`), overriding built defaults. Routing variants can be measured
   with zero code changes and nothing committed.

## Phase 0 — Preconditions and traps

- [ ] **Rule out the ICE trap first.** `LIVEKIT_NODE_IP` in
      `livekit-server/docker-compose.local.yml` defaults to `192.168.0.39`; if DHCP has
      moved the host, every session dies on an ICE timeout ~14 s in — suspiciously
      close to the greeting's 14,187 ms. Check the host's current LAN IP against the
      compose file before believing any measurement.
- [ ] Services up: manager API on :8002 (base path `/toy`), LiveKit on :7880, admin
      dashboard on :4000. In PowerShell use `curl.exe`, never `curl`.
- [ ] Worker rebuilt if any Go change is made:
      `go build -o picoclaw-livekit.exe ./cmd/picoclaw-livekit`. Persona changes live in
      the DB, no restart; API changes need the :8002 process restarted.

Traps carried from the handoff, binding for every later phase:

- **Never rename or reflow `MEMO: type=daily_quiz` lines.** `quiz_state.go` matches the
  literal. Any prompt trimming keeps every MEMO-syntax line byte-identical and the
  `daily_quiz` token count unchanged (7 in Riddler's `system_prompt`, 2 in
  `greeting_prompt`).
- `TestSynthesizeAndPlayLogsTTSProviderType` fails on a clean tree — pre-existing, do
  not chase.
- Verify from DB rows and workspace state files
  (`C:\Users\rahul\.picoclaw\workspace-device-<mac>\memory\state\`), not logs.
- Known instrumentation gap: the streaming request never sends
  `stream_options: {"include_usage": true}`; token counts only work because OpenRouter
  volunteers usage. Switching providers mid-test silently zeroes token counts
  (`docs/plan-usage-tracking-and-limits.md` §1.1). `avgTtftSeconds` is always 0.

## Phase 1 — Build the feedback loop

The loop is the deliverable of this phase; nothing else proceeds without it.

**Shape:** one script (`scripts/latency-loop.ps1` or `.py`, throwaway, not committed
until proven useful) that:

1. Starts a session headlessly:
   ```bash
   cd D:/cheeko-backend && python client.py --character-id cheeko --device-mac 00:16:3e:ac:b5:38
   ```
   (`--character-id` matches `ai_agent_template.agent_name` case-insensitively,
   session-scoped via `persist:false`. Valid: `riddler`, `quizzy`, `Cheeko`, `Tara`,
   `Mitthu`, `Nani`, `Masti`, `Chanda`.)
2. Parses the worker's **existing** turn latency summary —
   `pkg/livekit/audio_pipeline.go` `logTurnLatency()` ~line 534, per-turn summary
   ~592/603. No new timing harness. Markers: `stt_first_partial`, `stt_first_final`,
   `llm_first_token`, `tts_first_audio`, `tts_final_audio`, `turn_total_e2e`,
   `missing_*_marker` booleans.
3. Also captures, per LLM request: the `LLM request config` line
   (`agent_bridge.go:706`) and the `routed to upstream provider %q` line
   (`provider.go:377`) — **every latency sample gets labeled with the upstream that
   served it.** This makes the routing hypothesis readable directly from the data.
4. Prints one row per turn: session #, turn #, upstream, all markers.

**Red condition:** greeting `llm_first_token_ms` > 2,000 ms.

**Noise handling:** latency is inherently non-deterministic — a single run is not a
verdict. Each configuration gets **5 sessions**; report median and worst. The loop is
"deterministic" in the skill's sense when the verdict (red/green at the threshold,
on the median) is stable across batches.

**Character choice:** measure **Cheeko first** (no bank fetch, no rendered question
block), then Riddler. The difference between them is the bank path's contribution —
Quizzy/Riddler add a speculative bank fetch and a ten-question block to the greeting,
which is exactly the variable to isolate.

Phase 1 exit: the loop command has been run at least once, output pasted, and it shows
the red condition on the current config.

## Phase 2 — Baseline

Run the loop, unmodified config, before changing anything:

| Config | Character | Sessions | Record |
|---|---|---|---|
| As-is (unpinned) | Cheeko | 5 | median/worst per marker + upstream per request |
| As-is (unpinned) | Riddler | 5 | same |

Expected artifacts of this phase:
- Confirmation (or refutation) that greeting TTFT is red and correlates with which
  upstream served the request.
- Cheeko-vs-Riddler delta = bank path cost.
- Turn-2 numbers as the "unremarkable" control.

If the baseline does **not** reproduce the 14 s greeting: stop, re-check Phase 0's ICE
trap, and re-read the original session log before proceeding — do not bisect a bug that
isn't red.

## Phase 3 — Ranked hypotheses

Each with the falsifiable prediction the loop will test. Rank reflects expected payoff
given the Phase 2 labels.

1. **Unpinned price routing sends the greeting to slow/cold upstreams.**
   If true: setting an explicit provider (`OPENROUTER_PROVIDER_ORDER=DeepInfra` env, or
   `extra_body.provider.sort="latency"`) collapses greeting TTFT from ~14 s to ~1 s,
   and the routed-provider label stops varying. If false: pinned runs stay red.

2. **Cold ~5k-token prefix on the greeting** (persona 12,654 + soul 5,867 +
   greeting_prompt 2,371 chars, plus rendered bank block, plus 7 tool defs — nothing
   cached on the first request).
   If true: with the provider pinned, back-to-back sessions show the second greeting
   markedly faster (warm upstream KV cache), and stubbing the bank block shrinks TTFT
   roughly in proportion to tokens removed. If false: prefix size changes move TTFT
   only marginally.

3. **Ticket 002's overlap fix regressed or never covered this path.** The bank fetch
   deliberately runs *before* the persona pull so the two overlap — that overlap is
   what removed a 24 s first-audio tail (`docs/issues/riddle-bank/002-riddler-speaks.md`
   Resolution; speculative-fetch site `cmd/picoclaw-livekit/main.go` ~615, gate
   `livekit.PromptWantsQuizBatch`).
   If true: wall-clock timestamps show fetch and persona pull serialized, and a large
   gap between session start and the LLM request leaving. If false: the LLM request
   leaves promptly and the 14 s sits between request and first SSE byte.

4. **Marker artifact — the `llm_first_token` clock starts before media settles.**
   If true: marker deltas disagree with raw log timestamps around the same events.
   Related open question (parked, not this bug): turn 2's `turn_total_e2e_ms=19,988` vs
   `llm_final_token_ms=6,472` — ~13 s post-LLM, possibly `finalize_reason=
   turn_done_callback` including the child's thinking time. Confirm the span before
   treating as defect.

5. **`tools=7` prefix bloat.** Minor prefill multiplier, only material on slow
   upstreams. The bank characters' `system_prompt` explicitly forbids tool calls —
   verify that text before cutting anything.

## Phase 4 — Bisect, one variable at a time

All variants via `extra_body` on the model's provider config (`config.go:978`) or the
env var — **no code committed during measurement**. 5 sessions each, Cheeko, then the
winning variant re-run on Riddler.

| # | Variant | Mechanism | Tests hypothesis |
|---|---|---|---|
| 1 | Pin order | `OPENROUTER_PROVIDER_ORDER=DeepInfra` | H1 |
| 2 | Latency sort | `extra_body: {"provider": {"sort": "latency", "allow_fallbacks": true}}` | H1 |
| 3 | Latency cutoff | add `"preferred_max_latency": {"p90": ...}` to #2 | H1 (tail) |
| 4 | Warm-cache pair | winning routing variant, 2 sessions back-to-back | H2 |
| 5 | No tools | winning routing variant, tool defs dropped for the session | H5 |
| 6 | Stub bank block | winning routing variant, Riddler with bank block stubbed | H2 (bank share) |

If #1–#3 all stay red, H1 is dead: instrument the HTTP layer with a tagged log
(`[DEBUG-lat1]` request-sent → first-SSE-byte) to split "our pre-request time" from
"provider TTFB", which separates H3 from H2. One breakpoint/log at that boundary beats
scattering logs — tag everything for one-grep cleanup.

## Phase 5 — The fix

Shaped by Phase 4's winner; the expected shape given current evidence:

**5a. Make latency routing the default for OpenRouter, not env-gated** (~10 lines in
`buildRequestBody`, `provider.go:110–128`):

- apiBase is openrouter → always send a `provider` block.
- `OPENROUTER_PROVIDER_ORDER` set → `{"order": [...], "allow_fallbacks": true}`
  (current behavior, precedence preserved).
- Env unset → `{"sort": "latency", "allow_fallbacks": true}` — replacing today's
  silent fallback to price routing. Per Rahul's directive, price is not a
  consideration; sorting disables OpenRouter's price load-balancing by design.
- Consider `preferred_max_latency` p90 if Phase 4 #3 showed tail improvement.
- `extra_body` still merges last, so per-deployment overrides keep working.

**5b. Pin the fast upstream in dev env** (`OPENROUTER_PROVIDER_ORDER=DeepInfra` in the
local worker env, matching what k8s already does) — pinning is also the caching fix:
a stable upstream keeps its prefix KV cache warm. Verify the assembled prompt prefix is
byte-stable across sessions (no timestamps/dates early in the prefix; note
`memory/state/*.md` is injected wholesale and MEMO lines carry `date=` — confirm where
in the prompt they land).

**5c. Follow-ups, separate commits, ordered by safety:**
1. `stream_options: {"include_usage": true}` guard while the request builder is open —
   closes the silent-usage gap (plan-usage-tracking §1.1). One line plus test.
2. Drop the 7 tool defs for bank characters — only after verifying the prompt forbids
   tools in as many words. Free prefix reduction on every turn.
3. Stop injecting `quiz_bank.md` into non-bank characters (`ReadStateFiles` has no
   character filter — known issue #2, Cheeko currently receives riddle answers as
   prefix bloat). Needs its own small design decision; may become a ticket.
4. Greeting prompt trimming — **last**, highest risk (MEMO byte-identity), and only if
   Phase 4 #6 showed the bank block matters after routing is fixed.

**5d. Only if still too slow after 5a/5b:** faster serving stacks (`sort:
"throughput"` / `:nitro`, or premium hosts — Groq/Cerebras-class). This changes which
weights serve the child, so it rides on the Hinglish quality A/B
(`docs/cheeko-pricing-strategy.md:143`) — flag, don't do.

## Phase 6 — Regression pinning and cleanup

- [ ] Extend `pkg/providers/openai_compat/provider_routing_test.go`: env unset →
      request body carries `provider.sort == "latency"`; env set → `order` wins;
      non-OpenRouter base → no `provider` block. (Latency itself is not
      unit-testable; the request body is the correct seam.)
- [ ] Re-run the Phase 1 loop on the original scenario (Riddler greeting, 5 sessions):
      green at the 2,000 ms threshold on the median.
- [ ] `grep -r "DEBUG-lat1"` returns nothing; throwaway harness scripts deleted or
      moved under a marked debug location.
- [ ] Full `go test ./...` — green apart from the known
      `TestSynthesizeAndPlayLogsTTSProviderType`.
- [ ] Commit message states the confirmed hypothesis and cites the before/after
      medians.
- [ ] Update `deploy/README.md` / 008's promotion checklist if the env-var semantics
      changed (the "pin is inert" warning may become obsolete).

## Success criteria

1. Greeting `llm_first_token_ms` median < 2,000 ms across 5 Riddler sessions, worst
   < 4,000 ms.
2. Every request logs a routed upstream consistent with the configured policy.
3. Turn-2 TTFT does not regress.
4. No MEMO/state-file behavior change: `daily_quiz.md` still written, verdicts still
   attributed (spot-check one session's DB rows + workspace state files).

## Results (2026-08-07)

Shipped: `52cce92` (sort=latency default), `6c33799` (model-id normalization),
`7daa14a` (removed the k8s Crusoe,CoreWeave pin).

Greeting, live Riddler sessions, versus the 2026-08-06 baseline:

| Marker | Baseline | Best observed | Median of 5 |
|---|---|---|---|
| `llm_first_token_ms` | 14,187 | 1,144 | **2,863** |
| `tts_first_audio_ms` | 26,342 | 2,456 | ~4,400 |
| `turn_total_e2e_ms` | 42,475 | 12,452 | ~14,600 |

**Success criterion 1 was NOT met.** Target was a median under 2,000 ms and a
worst under 4,000 ms; actual is median 2,863 ms, worst 5,046 ms. The 5x
improvement is real, the bar is not cleared. Criteria 2-4 (consistent routing,
no turn-2 regression, MEMO/state behaviour unchanged) hold.

**Routing is no longer the bottleneck; prompt size is.** Two findings force this:

1. Isolated, the same model on the same prompt returns a first token in
   ~1.0-1.7s median regardless of routing config. The live greeting does not,
   and the difference is what the greeting carries.
2. The greeting's message count grows session over session on one device MAC -
   11, 15, then 24 across three consecutive runs - and the slowest greeting
   (5,046 ms) is the one with 24 messages. `tools=0` throughout, so tools are
   correctly excluded and are not a factor.

### History size, measured 2026-08-07 (corrects the paragraph above)

The "prompt size is the leading suspect" call was made from a three-point
correlation and is **only partly right**. Measured directly, doubling the
greeting prompt costs far less than that correlation implied:

| Greeting variant | Messages | Chars | TTFT median |
|---|---|---|---|
| Persona only | 2 | 20,725 | 1,214 ms |
| Persona + summary + restored history | 16 | 41,436 | 1,549 ms |

**+335 ms for 2x the prompt.** Real, worth reclaiming, but it does not explain
the 2,863 ms live median. Roughly a second in the worker path is still
unaccounted for by either routing or prompt size, and that is the open question.

What is confirmed about history:

- It **persists on disk per device**, at
  `workspace-device-<mac>/sessions/livekit_device_<mac>.jsonl`, and is reloaded
  into every later session. So a greeting is only a "first turn" in name.
- On the test device it held 13 messages / 20,396 chars (~5,100 tokens),
  roughly doubling the greeting prompt, under a session key created
  **2026-06-29** - over a month of carry-over.
- Summarization is working, not broken: the meta file has a summary and history
  is compacted at the threshold of 20. The floor it compacts to is what
  persists, and that floor is re-sent forever.

Two things ruled out, so nobody re-derives them:

- **`memory/MEMORY.md` is NOT injected**, despite being the largest file in the
  workspace at 27,965 bytes. `buildStaticContext` injects AGENT.md, SOUL.md,
  USER.md and IDENTITY.md; MEMORY.md appears only as a path the model is told
  to write to. Trimming it would buy nothing.
- **Tools are already excluded from the greeting** (`tools=0` in every live
  greeting), so handoff lead 5 is closed.

The question worth asking is not "how do we compress history" but **why a new
session replays a month-old transcript at all**, when USER.md and MEMORY.md
already exist as the durable-memory mechanism and a summary is already computed.

Provider pinning is also less trustworthy than the k8s comment implied: a
request pinned with `order: ["Cerebras"]` and `allow_fallbacks: true` was
served by DeepInfra. A pin names a preference, not a guarantee, so any
conclusion attached to a provider name needs the routed-provider log line to
back it up.

## Parked (out of scope, recorded so they don't get lost)

- Turn 2's ~13 s post-LLM `turn_total_e2e_ms` gap — confirm what the marker spans
  before treating as a defect.
- `avgTtftSeconds` always 0.
- MAC normalization gap in `macFilter` (task chip already spawned).
- `character_id=003c9c63-…` matching no template row — undiagnosed.
- Riddler prompt rewrite exists in dev DB only; backup at
  `C:\Users\rahul\AppData\Local\Temp\riddler-prompt-backup-2026-08-06.json`; belongs in
  005's checklist.
- Credential rotation: a DB connection string hit the transcript 2026-08-06 — rotate.
