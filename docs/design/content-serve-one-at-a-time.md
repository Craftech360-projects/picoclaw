# Serve unscored content one item at a time

Design, 2026-08-20. Not built — agreed to defer until character testing is
finished. Supersedes the transcript-matching approach shipped the same day
(`pkg/livekit/content_delivery.go`), which this would delete.

Applies to the unscored banks only: `joke` (Masti), `why` (Tara), `word`
(Mitthu), `spell` (Tikku). Story (Nani) is already one-per-session and keeps its
own completion gate.

---

## 1. The problem, stated once

The server hands out content. Only the character knows which of it was used.
There is no reliable channel back.

Masti is served six jokes, tells two, the child hangs up. The server cannot tell
which two. Every mechanism tried so far picks a different way to be wrong:

| approach | fails when |
|---|---|
| mark all six seen | four unheard jokes are spent — measured 67% loss on 2026-08-20 |
| trust the MEMO's `jokes_told` | model truncates or omits; Tara's MEMO names no items at all |
| match the transcript text | session runs in Hindi — the joke was translated, nothing matches |

The last one ships today. It is precise for English and **fails 100% of the time
in any other language**, marking nothing seen so the same jokes return forever —
a worse outcome than the burn it fixed, for exactly the languages this product
is built for.

## 2. Why batching is the actual cause

All three failures come from one decision: **serving more than the session
consumes.** The gap between "served" and "told" is what nobody can measure.

Close the gap and it stops needing measuring. Serve one item, and served *is*
told — the character asked for it because it was about to use it.

This is smaller than what it replaces. `content_delivery.go`, its probe table,
the run matcher and its eleven tests all go away.

## 3. Design

**Serving.** `GET /content-bank/next` returns **one** item — the least recently
seen unseen one — and marks it seen in the same call. `perDay` disappears from
`CONTENT_BANKS`.

**Asking.** The character needs a way to ask for the next one mid-session, which
is the one hard part: these characters are deliberately toolless
(`liveKitToollessCharacters`), because a tool call mid-turn costs a round trip
while a child waits and leaked call syntax has muted a turn before.

So the worker asks, not the model. The prompt already ends every turn with a
MEMO; the worker parses it anyway. When the MEMO shows the current item is
finished (`jokes_told` grew, or a per-bank equivalent), the worker fetches the
next item in the background and injects it into the state file before the next
turn. No new tool, no added turn latency — the fetch overlaps the child's reply.

**Prefetch of one.** Hold exactly one item ahead so the character is never
waiting on HTTP mid-sentence. A prefetched item that is never reached is the
only remaining waste: at most one per session, versus four today.

## 4. Exhaustion

Reaching the end of a bank is normal for a daily player — 60 jokes at 3–5 a
session is 12–20 sittings — and it is a **content** signal, not a runtime error.

**Serve least-recently-heard first**, and tell the character it is a repeat so it
frames one deliberately: *"Remember this one? Still my favourite!"* A repeat the
character owns is a callback; one it does not notice reads as a bug. The scored
banks already do exactly this — champion replay tells Quizzy to *"celebrate that
the child has already cracked every level and frame the round as a victory
lap."* Same flag, same intent.

`RECYCLE_AFTER_DAYS` (45) is then redundant: least-recently-heard ordering
handles freshness continuously instead of by cliff.

**Surface exhaustion where someone will see it** — a log line and a dashboard
count of children who have finished a bank. The real fix for an empty bank is
authoring more content, and nobody will know to do that from a silent recycle.

STARTER MODE (the character invents its own) stays as the API-down fallback
only. Invented jokes are unvetted; they are an emergency, not a steady state.

## 5. What changes

| file | change |
|---|---|
| `contentbank.service.js` | `nextContent` returns one item, marks it seen, orders by least-recently-seen; add `repeat` flag when recycling |
| `contentbank.routes.js` | unchanged shape, single-item payload |
| `content_bank.go` | render one item; add mid-session refetch driven by MEMO progress |
| `character_progress.go` | drop the served-codes reporting — the serve call records it |
| `content_delivery.go` + test | **delete** |
| prompts | note that a `repeat` item is a deliberate callback |

## 6. Risks

**A crashed session loses its prefetched item.** One item, bounded, acceptable.

**The MEMO still drives the refetch trigger.** If it omits progress the character
simply keeps the current item rather than advancing — it repeats nothing and
loses nothing, which is a safe failure. This is deliberately a weaker dependency
on the MEMO than tracking *which* items were told.

**More HTTP calls per session.** Three to five small local calls instead of one;
they overlap the child's turn and never block speech.

## 7. Verification

- A session that tells two jokes marks exactly two seen (the 2026-08-20 replay,
  which currently marks six).
- A Hindi session marks correctly — no text matching is involved, which is the
  whole point.
- Exhausting a bank serves the oldest-heard item with `repeat: true`, never
  silence.
- A session ending mid-joke spends at most one prefetched item.
