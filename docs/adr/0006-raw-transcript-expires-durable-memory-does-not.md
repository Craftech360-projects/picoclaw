# The Raw Transcript Expires; Durable Memory Does Not

A device's stored conversation is replayed into every later session, so a
greeting is a first turn in name only — the test device carried turns from
2026-06-29 into an August greeting. **We reset the raw transcript when a session
starts more than a set gap after the previous one ended, and keep everything
else.** Summaries, `USER.md`, `MEMORY.md` and the Saved State MEMO are
untouched, so what the toy remembers about a child does not change.

This exists because one switch governs two things with different lifetimes.
`preserveWorkspace = !managerBacked` is a deliberate choice about who owns
durable session state, and with the manager-backed store off (the default) the
device workspace is that store. The transcript merely shares the directory with
`USER.md` and `MEMORY.md`, so preserving memory preserves the transcript too.
Nobody chose month-old replay; it arrived with the feature that was meant to
persist.

The gap threshold keeps genuine reconnects working. A dropped connection
resuming inside the window still gets its context, which is the one failure mode
worth protecting against — a child should never have to repeat themselves
because the wifi blinked. This extends the existing 45-second reconnect hint
rather than contradicting it: that hint governs workspace deletion during a
handoff, this governs transcript age across visits.

## Considered Options

**Always start clean** was rejected for that reconnect case alone; it is
otherwise the best option on latency and retention.

**Capping replay length** treats a month-old turn as equal to a recent one, so
it caps the symptom and not the cause. Acceptable stacked on top of expiry,
not instead of it.

**Enabling the manager-backed session store** takes the other branch of
`preserveWorkspace` outright. It is a legitimate direction, but it relocates the
retention question to the manager rather than answering it, and is a much larger
change than a latency fix should carry. Left open on its own merits.

**Accepting the replay** was rejected on two grounds. The measured cost is small
— +335 ms of first-token latency — but a month-old verbatim recording of a
child's conversation persisting with no expiry is a data-retention position we
do not want to hold by accident.

## Consequences

- Quizzy and Riddler improve. Both prompts already carry defensive wording that
  chat history does **not** indicate quiz progress, because replayed history was
  misleading them; their resume runs off the `daily_quiz` MEMO, which survives.
- Cheeko keeps its personalisation, which reads `## Session Summaries` from
  `MEMORY.md`, not the transcript. It loses only the ability to avoid repeating
  greeting wording across visits — already failing today, as the stored
  transcript contains two identical consecutive greetings.
- Nani is unaffected; it reads only `USER.md`.
- **Cross-character contamination is reduced, not fixed.** The session key is
  device-scoped, so one transcript is shared by every character on a device and
  a character switch inside the window still leaks. Scoping per character-device
  pair remains open.
- Anyone changing the threshold must keep it comfortably above the 45-second
  reconnect hint, or a handoff will land on the wrong side of the reset.
