---
status: proposed
assignee: unassigned
---

# 004 — Backfill and live verification

## Parent

`docs/issues/chat-history-attribution/000-design.md`

## What this covers

001–003 make new conversations correct. This ticket decides what happens to the
existing pile and proves the whole thing works from a real toy.

## The existing rows

Every `voice_session_messages` row written to date carries the device's default
character. **Per-message repair is impossible** — the row holds
`(session_id, mac_address, agent_id, sequence, role, content, created_at)` and
nothing else that names who spoke. The character is simply not recorded anywhere
on it.

Per *session* there is one partial source: `memory/MEMORY.md` in each workspace
carries `- <timestamp> [<character>] (N messages): …` bullets, written by
`persistSummaryToMemoryFile`. Those only exist for sessions that produced a
summary, and only since the labelling was added — earlier bullets are unlabelled.

So the realistic options are:

**A. Accept it (recommended).** Leave history before the deploy attributed to the
device default; correct attribution starts at the cutover. Record the cutover
timestamp so the app can caption older sessions honestly rather than showing them
under a character that may not have spoken them.

**B. Partial session repair.** Match `MEMORY.md` bullets to `voice_sessions` by
timestamp and message count, and correct `voice_sessions.agent_id` where a
confident match exists. Messages stay wrong, so a session would list under one
character while its rows claim another — worse than A unless the message rows are
updated from their session, which is a guess dressed as data.

**C. Delete pre-cutover history.** Clean, defensible on retention grounds, and
throws away real conversations parents may already have seen.

Pick one and write it in the resolution. Do not leave this implicit.

## Live verification

Run after all three are deployed to the dev box (never prod — see
`cheeko-deploy-boundaries`). One device, one child paired.

1. Talk to the default character. End the session.
2. Tap a card for a second character. Talk. End.
3. Tap a third. Talk. End.

Then check, per `session_id` and never "the most recent N" — a previous session's
teardown flush lands seconds into the next one
(`greeting-latency/001`, "Why the A/B could not be run back-to-back"):

```sql
SELECT s.session_id, a.agent_name, s.kid_id, count(m.*) AS messages
FROM voice_sessions s
JOIN ai_agent a ON a.id = s.agent_id
LEFT JOIN voice_session_messages m ON m.session_id = s.session_id
WHERE s.started_at > now() - interval '1 hour'
GROUP BY 1,2,3 ORDER BY min(s.started_at);
```

Expect three rows, three distinct `agent_name`s, one `kid_id`, and
`voice_session_messages.agent_id` matching its session on every row.

Then, in the worker workspace: three `sessions/*.jsonl` files, one per character,
and one `memory/MEMORY.md` carrying three labelled bullets.

## The toy-swap test — the reason for the whole plan

1. Note the child's `kid_id` and the sessions above.
2. Pair the same child to a second device (different MAC).
3. Hold one conversation on the new toy.
4. `GET /api/mobile/kids/:kidId/characters` and the per-character session list.

**Pass:** the list spans both MAC addresses; nothing is missing; the transcript of
an old session still opens.

Then the inverse, which is the one that actually protects a family:

5. Pair a *different* child to the first device.
6. Read that child's history.

**Pass:** empty. Not "empty-ish" — the previous child's sessions must not appear
under any character, and the new child's workspace must not restore the previous
child's transcript.

## Acceptance criteria

- [ ] Backfill option chosen, executed or explicitly declined, and recorded
- [ ] Cutover timestamp recorded somewhere the app can read or the docs can cite
- [ ] Three-character live run passes the SQL check above
- [ ] Per-character `sessions/*.jsonl` files confirmed in the workspace
- [ ] Toy-swap test passes in both directions (same child follows, new child sees
      nothing)
- [ ] Parent-app screens rendered against real data, not fixtures
- [ ] Result written into the resolution of each of 001–003, including anything
      that did not behave as the ticket predicted

## Blocked by

- `001-history-carries-the-speaking-character.md`
- `002-reads-scope-by-child-and-character.md`
- `003-per-character-transcript-shared-summaries.md`
