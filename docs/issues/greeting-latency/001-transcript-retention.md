---
status: open
assignee: unassigned
---

# 001 — Decide how long a device's raw transcript survives

## Parent

`docs/plan-llm-latency.md`

## The problem

A new voice session is not a fresh conversation. The greeting — nominally the
first turn — replays the device's stored transcript, so the model is re-read
the tail of previous visits before it says hello.

Measured on the test device 2026-08-07:

- `workspace-device-00163eacb538/sessions/livekit_device_<mac>.jsonl` held
  **13 messages / 20,396 chars (~5,100 tokens)**, roughly doubling the greeting
  prompt on top of the ~5,200-token persona.
- The session key's `created_at` is **2026-06-29** — over a month of carry-over.

**This is a decision ticket, not a bug report.** Nothing here is misbehaving
against its own design; two things with different retention needs simply share
one lifecycle. Someone has to choose what the toy should remember between
visits.

## Why it happens

`cmd/picoclaw-livekit/workspace_lifecycle.go:26`:

```go
managerBacked := managerAPIBaseURL(managerAPI) != "" && managerAPI.SessionStoreEnabled
preserveWorkspace = !managerBacked
```

This is a deliberate either/or about who owns durable session state. Manager
store on: the manager owns it, the local workspace is scratch, delete on close.
Manager store off: the local workspace *is* the durable store, so preserve it.

`session_store_enabled` is absent from config and defaults to `false`
(`pkg/config/defaults.go:531`), so every deployment today takes the preserve
branch.

The workspace is preserved so the toy remembers the child — `USER.md`,
`MEMORY.md`, the persona files. That part works as intended.
`sessions/*.jsonl` just happens to live in the same directory, so preserving
durable memory also preserves the raw transcript. One boolean governs both.

The 45-second reconnect hint (`pkg/livekit/workspace_handoff.go:59`) confirms
the gap: there is careful lifecycle thinking for "reconnected ten seconds
later" and none for "came back tomorrow". The system cannot distinguish a
**resumed connection** from a **new visit**.

It is bounded, not unbounded: summarization compacts at 20 messages
(`agent_bridge.go:1764`), so the transcript settles at a floor. That floor is
then re-sent on every greeting, indefinitely.

## Size of the prize — read before prioritising

Direct A/B, real persona, 5 samples each, latency-sorted routing:

| Greeting variant | Messages | Chars | TTFT median |
|---|---|---|---|
| Persona only | 2 | 20,725 | 1,214 ms |
| Persona + summary + restored history | 16 | 41,436 | 1,549 ms |

**+335 ms.** Real, but this is not the remaining latency problem — roughly a
second of the live 2,863 ms median is still unexplained by routing or prompt
size, and that belongs in its own ticket. Do not size this work as if it
recovers the gap.

There is a second, possibly stronger reason to care: a month-old verbatim
transcript of a child's conversation sitting on disk with no expiry is a data
retention question independent of latency. Whoever decides the policy should
weigh that, and it may outrank the 335 ms.

## Options

**A. Age-based reset of the transcript only (recommended).**
On session start, if the gap since the last session exceeds a threshold
(30-60 min), drop the stored messages and keep the summary. Reconnects inside
the window resume untouched.
*For:* smallest change, confined to the session-start path, leaves
workspace-preservation and `MEMORY.md` alone, and extends the reconnect-window
concept the code already has. *Against:* introduces a second time constant that
must not contradict the 45s hint; picking the threshold is a judgment call.

**B. Always start clean; rely on summary + `MEMORY.md`.**
Every session begins with no replayed turns.
*For:* simplest to reason about, best latency, strongest retention story, and
uses the durable-memory mechanism that already exists for exactly this.
*Against:* a genuine reconnect mid-conversation loses immediate context and the
child has to repeat themselves — the worst failure of the set, and the reason
option A exists.

**C. Cap the replay instead of ageing it (keep last N turns).**
*For:* trivial, bounds cost regardless of age. *Against:* treats a month-old
turn as equal to one from a minute ago; caps the symptom, not the cause. Fine
as a stopgap combined with A.

**D. Turn on the manager-backed session store (`session_store_enabled`).**
Takes the other branch outright: the manager owns session state and the local
workspace becomes disposable.
*For:* uses the design's intended path, and centralises state for multi-worker
deployments. *Against:* much larger change with its own retention question
merely relocated to the manager, plus whatever `workspace_sync` /
`workspace_restore` imply. Should be its own decision, not a latency fix.

**E. Accept it.** Legitimate if cross-visit continuity is judged worth 335 ms.
If chosen, say so in an ADR so this is not re-investigated a third time.

Recommendation: **A**, with the threshold written down and a comment tying it
to the 45s reconnect hint. Revisit **D** on its own merits.

## Acceptance criteria

- [ ] A decision is recorded (ADR under `docs/adr/`) naming the chosen option
      and the retention window, whatever the choice — including E
- [ ] If A or C: a session starting after the window replays no stored turns,
      verified from the greeting's `messages=` count in the turn log
- [ ] A reconnect inside the window still resumes with context intact
- [ ] The summary and `MEMORY.md` survive in every case — this ticket must not
      weaken cross-visit memory, only the raw transcript
- [ ] Greeting TTFT re-measured after the change; expect roughly -335 ms, and
      record it rather than assuming it
- [ ] `MEMO: type=daily_quiz` handling unaffected — quiz/riddle verdicts still
      attribute correctly after a reset session (see the traps in the parent plan)

## Do not re-derive these

Checked on 2026-08-07, all dead ends:

- **`memory/MEMORY.md` is not injected into the prompt.** It is the largest file
  in the workspace (27,965 bytes) and grows every session, but
  `buildStaticContext` (`pkg/agent/context.go:458`) injects AGENT.md, SOUL.md,
  USER.md and IDENTITY.md only; MEMORY.md appears solely as a path the model is
  told to write to. Trimming it buys nothing.
- **Tools are already excluded from the greeting** — `tools=0` in every live
  greeting, dropped at `agent_bridge.go:685`.
- **Summarization is not broken.** The meta file carries a real summary and
  history compacts at the threshold. The floor it compacts to is the issue.
