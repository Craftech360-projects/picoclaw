---
status: open
assignee: unassigned
---

# 002 — The ~1s of greeting latency that routing and prompt size do not explain

## Parent

`docs/plan-llm-latency.md`

## The gap

After latency routing shipped (`52cce92`), greeting first-token dropped from
14,187 ms to a 2,863 ms median over five live sessions. Measured in isolation,
the same model on an equivalent prompt answers far faster:

| Measurement | TTFT median |
|---|---|
| Isolated request, persona only | 1,214 ms |
| Isolated request, persona + summary + full restored history | 1,549 ms |
| **Live greeting, 5 sessions** | **2,863 ms** (worst 5,046) |

So roughly **1,300 ms is unaccounted for**. Routing is fixed and prompt size is
measured at +335 ms (ticket 001), and neither closes this.

The plan's success criterion — median under 2,000 ms, worst under 4,000 ms — is
still unmet. Closing this gap is what meets it.

## Do this first: the marker cannot currently tell you where the time goes

`llm_first_token_ms` is measured from `LLMStart`, set at
`pkg/livekit/audio_pipeline.go:1211` **before** `GenerateGreeting` is called.
Everything below is inside that number, not just the network call:

1. `renderPromptPlaceholders` and `RenderQuizQuestions`
2. `AddFullMessage` of the greeting instruction
3. `GetHistory` / `GetSummary`
4. `buildMessages` → `ContextBuilder.BuildMessages`, which reads `AGENT.md`,
   `SOUL.md`, `USER.md` and `MEMORY.md` (27,965 bytes on the test device) plus
   `memory/state/*.md` from disk and assembles a ~50 KB string
5. `acquireSessionLLMSlot` — a mutex polled on a **10 ms ticker**
   (`agent_bridge.go:1214`), so an uncontended acquire is free but a contended
   one quantises to 10 ms steps
6. HTTP request, TLS, and only then time-to-first-SSE-byte

**There is no marker between "request built" and "first byte", so worker-side
time and provider-side time are currently indistinguishable.** Add one probe at
that boundary before theorising further — it splits the 1,300 ms in two and
decides which half of the list below to chase. Everything else here is
speculation until that exists.

## Measured 2026-08-07, after the probe landed

27 warm live Cheeko greetings, two worker processes, `sort: latency` routing:

| | median | worst |
|---|---|---|
| `llm_request_build_ms` (worker-side: render, history, `BuildMessages`, slot) | **82 ms** | 177 ms |
| `llm_provider_ttft_ms` (dispatch → first token) | **993 ms** | 2,339 ms |
| `llm_first_token_ms` (the two summed) | **1,115 ms** | 2,419 ms |

**Two things follow, and both contradict what this ticket assumed.**

1. **Worker-side cost is ~80 ms, not seconds.** Hypothesis 3 (context assembly)
   is closed by its own stated test — it predicted "if it is under ~50 ms, close
   this branch"; 82 ms total covers assembly *plus* history, slot wait and
   marshalling, so no single part of it is worth optimising. Hypothesis 5 (slot
   contention) is closed with it: there is no room for a meaningful wait inside
   82 ms.
2. **The gap this ticket exists to explain did not reproduce.** The premise was a
   2,863 ms live median against a 1,214–1,549 ms isolated one. Today the live
   median is 1,115 ms — at or below the isolated figures, with the same routing.
   Whatever produced the 1,300 ms is not present in these 27 samples.

The parent plan's criterion (median < 2,000 ms, worst < 4,000 ms) is **met** by
this run: 1,115 ms median, 2,419 ms worst.

**Do not close this ticket on one afternoon's numbers.** The measurement that
motivated it was real, and a provider-side effect that comes and goes — which is
what hypotheses 1 and 2 describe — would look exactly like this. What the probe
has established is where to look when it returns: `llm_provider_ttft_ms` is now
separately visible, so a recurrence can be attributed without re-deriving any of
this. Cold start (hypothesis 2) was also milder than recorded: the first greeting
after each worker start was 1,827 ms and 1,568 ms, against a 1,115 ms warm
median — a few hundred ms, not the ~2,900 ms measured previously.

## Hypotheses, ranked, each with its prediction

**1. Prefix-cache misses from upstream bouncing.** `sort: latency` was observed
routing the same device to Cerebras, Together and DeepInfra across consecutive
requests. `provider.go` already documents the cost: *"each upstream caches
separately, so bouncing between them wastes the byte-stable prompt prefix."*
That warning was written to justify pinning, and `52cce92` removed pinning — so
this ticket may be partly self-inflicted, trading cache warmth for routing
speed. **Prediction:** pinning one upstream via `provider.order` and repeating
the same greeting shows the second and later requests markedly faster than the
first, while latency-sorted routing shows no such warm-up. If true the fix is a
stickiness policy, not a return to a static pin.

**2. Cold start per worker process.** Measured: first greeting after a worker
start 4,626 ms versus 1,740 ms warm — about 2,900 ms of first-request cost
(DNS, TLS, connection pool). This is confirmed to exist; what is unknown is
whether a residue persists past the first request. **Prediction:** pre-warming
the HTTP client at worker start removes the first-session penalty and moves the
median only slightly. Worth doing regardless — the first child to connect after
every rollout currently pays it.

**3. Context assembly cost per request.** ~50 KB read from disk and concatenated
on every turn, inside the measured window. **Prediction:** timing the
`BuildMessages` call alone accounts for a visible slice; if it is under ~50 ms,
close this branch and do not optimise it.

**4. The dynamic context defeats prefix caching by design.** `context.go:160`
records that state files in the static block gave *"a different cache prefix on
every request"* and *"greeting TTFT up to 26s"*, fixed by moving memory into the
dynamic per-request context. But dynamic content still sits in the prompt, and
if it lands **before** any large stable region, the cacheable prefix ends there.
**Prediction:** dumping the assembled prompt for two consecutive greetings and
diffing shows where the first byte differs; if that offset is early, the stable
prefix is small no matter how large the persona is.

**5. LLM slot contention.** Cheapest to rule out. **Prediction:** logging the
wait inside `acquireSessionLLMSlot` shows ~0 ms on the greeting path, since
nothing else holds the session lock at session start.

## Acceptance criteria

- [x] A marker exists between request-built and first-SSE-byte, so worker-side
      and provider-side time are separately visible in the turn summary —
      `llm_request_build_ms` and `llm_provider_ttft_ms` in `Turn latency summary`,
      fed by `protocoltypes.WithDispatchProbe`. Both read 0 on providers that do
      not call `ReportDispatch` (only `openai_compat` does).
- [ ] The ~1,300 ms is attributed — not necessarily fixed — with numbers
      supporting the attribution
- [ ] Greeting TTFT median under 2,000 ms and worst under 4,000 ms across 5
      live sessions, the criterion still open from the parent plan
- [ ] If hypothesis 1 holds, the routing decision from `52cce92` is revisited
      with cache warmth measured, not assumed, and the outcome recorded in an ADR
- [ ] Cold-start cost is either removed or documented in `deploy/README.md`, so
      the first session after a rollout is not misread as a regression

## Tooling that already exists

- `pkg/providers/openai_compat/latency_harness_test.go`, behind the
  `integration` build tag. Measures the shipped default plus price, throughput
  and explicit-pin arms against a real persona fixture, labelling every sample
  with the upstream that served it. Run:
  `OPENROUTER_API_KEY=… LATENCY_FIXTURE=<persona.json> go test -tags integration -run TestLatencyHarness -v ./pkg/providers/openai_compat/`
- Live sessions need no hardware:
  `python client.py --character-id cheeko --device-mac <mac>` from
  `D:\cheeko-backend`, with `TEST_SERVER_IP` / `TEST_MQTT_BROKER_HOST` /
  `TEST_MANAGER_API_BASE` pointed at localhost. **`MANAGER_API_BASE` defaults to
  the production box — always override it.**
- Measure Cheeko before a bank character: Quizzy and Riddler add a speculative
  bank fetch and a rendered question block, which is noise for this ticket.

## Notes

- Use **warm** sessions for the headline number and report cold separately;
  mixing them produced the 5,046 ms worst case and made the median look worse
  than steady-state behaviour.
- Latency here is noisy. Five samples minimum per configuration, report median
  and worst, and never conclude from a single run — an earlier session in this
  investigation drew a wrong conclusion from a three-point correlation.
- Ticket 001 (transcript retention) is independent and worth roughly 335 ms. It
  does not close this gap and neither ticket blocks the other.
