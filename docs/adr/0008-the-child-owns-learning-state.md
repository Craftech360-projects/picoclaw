# The Child Owns Learning State, Keyed By A Namespaced Owner Key

Every durable thing a child builds up — chat history, `MEMORY.md`, the workspace,
Question Bank progress, the images they imagined — is stored against the toy's MAC
address. Replace the toy and the child starts from nothing; hand the toy to a
younger sibling and that sibling inherits an eight-year-old's memories and quiz
level. **Learning state moves to the Child, addressed by a single non-null
`owner_key` column holding `kid:<id>` when the Device is paired, or
`mac:<address>` when it is not.** The Device keeps only what is about the
hardware: settings, battery, firmware, and the entitlement to run sessions.

The key is a namespaced string rather than a nullable `kid_id` because Postgres
treats NULLs as distinct in a unique index. `UNIQUE(kid_id, relative_path)` gives
an unpaired Device no uniqueness at all — it may write ten `MEMORY.md` rows — and
Prisma's compound-unique `where` cannot take a null, so every one of the seven
artifact and memory upsert sites would need a read-then-branch instead of the
one-line key swap it needs today. The string has no null case, so both paths are
the same code.

That choice also removes a guard nobody would remember to write. With a nullable
`kid_id` plus a MAC fallback, an unpaired Device reads `device_mac = A` and gets
back the previous child's rows — the sibling hand-me-down leak, reintroduced by
the fallback meant to be safe. Every read and every adoption statement would need
`AND kid_id IS NULL` appended, correctly, forever. With namespaced keys a row
stamped `kid:123` is simply not in the `mac:` namespace and no MAC-scoped query
can reach it. The isolation is the key value, not a filter.

**Reassigning a toy therefore needs no cleanup step.** An earlier draft of this
work had a transactional handover function that cleared the outgoing child's
rolling summary, bumped the custom-card version so the toy would drop its cached
audio, and repointed the external memory service. Each of those was compensating
for a store still keyed by device. Keyed by child, reassignment is one column
change and every store simply begins writing under a different key; the outgoing
child's data is untouched and intact if they are ever paired to a Device again.
`ai_agent.summary_memory` moves into the owner-keyed memory documents rather than
gaining a column, because `ai_agent` is the Character row and is shared across
children. Custom cards move onto a child-keyed pack table; `custom_content_pack` exists in
the Prisma schema for exactly this purpose but its migration has never been
applied, so it must be created rather than adopted. With one `CUSTOM_<MAC>` pack
on the whole dev box, that work is safely deferrable. The device-side audio cache
needs no version bump either way: it refetches when the content hash moves, and a
different child's pack is different content.

The worker's workspace directory becomes `workspace-kid-<id>`, which requires the
gateway to put `kid_id` into room metadata — `resolveLiveKitWorkspaceLifecycle`
reads metadata and room name only, before any manager call. This is the sole
reason the change reaches beyond the manager API. It is worth it: a stale
`workspace-kid-7` left behind by a crash belongs to kid 7 either way, so the
directory cannot leak across children even when teardown does not run.

## Considered Options

**A transfer operation at bind time**, copying rows from the old MAC to the new,
needs no schema change and is roughly a tenth of the work. It fixes device
replacement and breaks on hand-me-downs: unbind, rebind to a sibling, and the
rows are still sitting on that MAC. Adding a park-on-unbind step to close that is
this decision with extra steps.

**Nullable `kid_id` with a MAC fallback** is the obvious shape and was the
working plan until the NULL-uniqueness and fallback-leak problems above surfaced.
It survives for `quiz_question_answer` and `riddle_question_answer`, which carry
no unique constraint over the key and so pay neither cost.

**Dropping `mac_address` outright** is the clean end state. It is load-bearing in
roughly sixty endpoints, the admin dashboard, and raw analytics SQL, and is not
reversible mid-flight. Both columns stay as denormalised audit fields, written on
every upsert and read by nothing.

**Making the pairing permanent** — one Child per Device forever — would remove the
problem instead of solving it, at the cost of the hand-me-down, which is a normal
thing families do with a toy.

## Consequences

- A Device with no Child is a normal, reachable state: `bindDevice` never set
  `kid_id`, and the documented activation sequence contains no assign call. It now
  auto-pairs when the owner has exactly one child, and the 409 on reassignment is
  dropped so the app's existing picker can always correct a wrong pairing.
- Rows written while unpaired carry `mac:<address>` and are adopted into
  `kid:<id>` when pairing happens. Adoption cannot steal an attributed row,
  because attributed rows are not in the `mac:` namespace.
- Existing rows are attributed to whichever Child the Device is paired to at
  migration time. A survey of DB1 (2026-08-12) found **no Device whose history
  predates its current Child, and no Child on more than one Device**, so on that
  data the backfill is provably lossless and needs no dedupe pass. It also found
  every MAC carrying data has an `ai_device` row, so nothing is unattributable.
- **`device_kid_assignment` should be added now, while there is no backlog.** No
  Device has changed hands yet, so nothing needs reconstructing — which is exactly
  why it is cheap today. Once one does, the imagine gallery and analytics rollups
  for that Device can never be split between the two Children, because no other
  attribution signal exists.
- Auto-pairing on bind is the highest-value line in this work: of 24 Devices, 6
  are paired and 17 have an owner with exactly one Child. Roughly ten lines takes
  pairing from 6/24 to 23/24, and the remaining one is the case the picker exists
  for.
- Only seven of the analytics rollups are live. Every `analytics_*` table plus
  `game_session`, `kid_activity_log` and `ai_agent_chat_history` has zero rows and
  zero lifetime inserts; `device_memories`, `memory_chunks` and
  `user_question_quota` are in the Prisma schema but **absent from the database**.
  Migrate the seven; drop the rest from the schema rather than carrying them.
- The imagine feed gains a table. It has none today — the gallery is an S3 prefix
  listing capped at 1000 objects with no continuation token. Keying it by child
  fixes the pagination cap as a side effect, and S3 keys never move.
- Billing stays on the Device by deliberate choice: usage is a fact about which
  unit burned minutes. A replacement toy therefore grants a fresh trial.
- The transcript becomes child-scoped, which narrows but does not close the
  contamination noted in [0006](0006-raw-transcript-expires-durable-memory-does-not.md):
  one transcript is still shared by every Character a child talks to.
- Mem0 is dropped rather than rekeyed. The gateway already returns empty memory
  arrays; only the manager still writes, under the MAC, mixing siblings into one
  profile.
