---
status: closed
assignee: claude
---

# 006 — Chat history attributes to the child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

`voice_sessions` already has a `kid_id` column and an index on
`(kid_id, started_at desc)`. The survey found **451 sessions with it null** — it is
declared, indexed, and largely unpopulated. Individual `voice_session_messages` rows
have no child reference at all and are reachable only through the session join, which
is correct and should stay that way: the message belongs to the session, the session
belongs to the child.

Populate `kid_id` on every session upsert, backfill the existing rows from
`ai_device`, and switch the history read paths to resolve by child, with the same
null-guarded MAC fallback used in 002.

There is no separate transcript table to migrate. `ai_agent_chat_history` is the
legacy xiaozhi table — zero rows and zero lifetime inserts on DB1, no writer in the
codebase, one residual reader that only uses it as a boolean personalisation signal.
It is removed in 009, not here.

The parent app's session list and the founder dashboard both read this data. Their
results should not change for a device that has only ever had one child, which is
every device on DB1 today — that is the regression test.

## Acceptance criteria

- [x] `voice_sessions.kid_id` is written on session upsert, from the device row —
      **both** paths now, `ensureVoiceSession` never loaded a device at all
- [x] Backfill written — **not run on DB1**, no deploy
- [x] Chat history and session lists resolve by child, falling back to MAC only for
      unpaired devices and only for rows with no child
- [x] A child moved to a new toy sees their earlier conversations
- [x] A sibling paired to a used toy sees none of the previous child's conversations
- [x] Reads for a device that has only had one child are unchanged in shape — the
      existing bootstrap test still passes on the same fixture
- [x] Message rows are still reached through the session, not given their own child
      column
- [ ] Verified from a live session — **deferred, no dev-box deploy**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md`


## Resolution

Shipped in `8ab0d013`.

The column and its index have existed since the table was created. Only one of the two
upsert paths ever populated them, which is why 451 sessions carry no child:
`ensureVoiceSession` never loaded the device row at all, so it could not have written
one. It does now.

One helper, `voiceSessionScope`, decides what a read matches, and the three bootstrap
reads spread it. Messages and summaries reach the child **through their session** via
the Prisma relation rather than gaining a child column of their own — a message belongs
to its session and the session to the child, and a second copy of that fact would be
free to drift out of agreement with the first.

**Scope grew by one read the ticket did not list.** `voice_session_summaries` was still
MAC-scoped in the bootstrap. It was found by an existing test asserting the old where
clause — not by reading the diff, and not by my grep, which had been aimed at
`voice_sessions` and `voice_session_messages`. Third time this phase that a missed call
site surfaced from a test rather than a sweep.

Full suite: **1390 passed, 67 suites.** Two new tests assert the paired and unpaired
cases directly, including that the fallback carries `kid_id: null` — without which a toy
handed on before the parent picks a child would replay the previous child's
conversations into the prompt.
