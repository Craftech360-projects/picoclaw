# Child-owned state — learning follows the child, not the toy

Design, 2026-08-12. Spans `D:\cheeko-backend\main\manager-api-node` (Node/Prisma),
`D:\cheeko-backend\main\mqtt-gateway` (one field), and `D:\picoclaw` (one case
statement).

A child's chat history, durable memory, workspace, Question Bank progress and
imagined images are all stored against the toy's MAC address. Replace the toy and
the child starts from nothing. Hand the toy down to a younger sibling and that
sibling inherits an eight-year-old's memories and quiz level. **This moves every
one of those to the child.**

Background reading, not repeated here:
`docs/adr/0008-the-child-owns-learning-state.md`, `CONTEXT.md` (**Child**,
**Device**, **Unlinked Device**, **Handover**).

---

## 1. The load-bearing observation

The MAC never has to leave the wire. The gateway sends a MAC at `hello`, the Go
worker sends a MAC on every manager call, and the firmware knows nothing else. The
manager already resolves MAC → kid on the hottest path in the system:
`quiz.service.js:42-54` loads `ai_device.kid_id`, uses it to pick the Age Band from
`kid_profile.birth_date`, and then **throws it away** except for one log line.

So content selection is already child-derived while progress storage is
device-derived. That inconsistency is the whole bug, and closing it is a
manager-side change everywhere except one thing: the worker names its workspace
directory from room metadata before it has spoken to the manager
(`workspace_lifecycle.go:23`, called at `main.go:443`), so a per-child directory
needs `kid_id` carried in the dispatch metadata.

## 2. Two keys, deliberately

| Tables | Key | Why |
|---|---|---|
| `quiz_question_answer`, `riddle_question_answer`, `voice_sessions`, analytics rollups | nullable `kid_id` | No unique constraint spans the key, so NULLs cost nothing |
| `device_workspace_artifacts`, `device_memory_documents`, `device_memory_chunks` | non-null `owner_key` — `kid:<id>` or `mac:<address>` | These have compound uniques. Postgres treats NULLs as distinct, so `UNIQUE(kid_id, path)` gives an unpaired device no uniqueness at all, and Prisma's compound-unique `where` cannot take a null |

The namespaced string also removes a guard nobody would remember to write. With a
nullable `kid_id` plus a MAC fallback, an unpaired device reads `device_mac = A`
and gets the previous child's rows back — the hand-me-down leak, reintroduced by
the fallback meant to be safe. A row stamped `kid:123` is not in the `mac:`
namespace, so no MAC-scoped query can reach it. The isolation is the key value,
not a filter someone can drop.

## 3. Decisions

| Decision | Choice | Why |
|---|---|---|
| Pairing | One Child per Device at a time, reassigned later | Matches what `assignKidByMac` already enforces; siblings do not share a toy simultaneously |
| Unpaired devices | Auto-pair on bind when the owner has exactly one kid; otherwise leave null and let the app's existing picker resolve it | Survey: 17 of 24 devices qualify. Creating a placeholder kid instead would collide with the app's own onboarding row and permanently 409-block the picker |
| The 409 | Deleted | `device.service.js:285-291` throws whenever `kid_id` is set and differs, so the picker can never correct a wrong pairing. Reassignment is expected, not an error |
| Backfill rule | Attribute all existing rows to the device's current child | Survey found zero devices whose history predates their current child, so this is provably lossless on DB1 |
| Rows written while unpaired | Adopted into `kid:<id>` on pairing | Cannot steal an attributed row, because attributed rows are not in the `mac:` namespace |
| Handover cleanup | None | Keyed by child from the start, reassignment is one column change; every store simply begins writing under a different key |
| `ai_agent.summary_memory` | Moves into `device_memory_documents` | `ai_agent` is the **Character** row, shared across children; a column there gives one summary per character, not per child |
| Mem0 | Dropped, not rekeyed | Gateway already returns empty memory arrays; `MEM0_API_KEY` is unset on the dev box |
| Billing | Untouched, stays device-keyed | Usage is a fact about which unit burned minutes. A replacement toy grants a fresh trial; accepted |
| Wire protocol | Unchanged except one metadata field | No firmware change, no mobile release required |

## 4. Survey — DB1 (`tsiocygczplmnjpqmutc`), 2026-08-12

Read-only. This is what makes the migration boring.

| Question | Result | Consequence |
|---|---|---|
| Devices by pairing state | 24 total: 6 paired, 17 with an owner who has exactly one kid, 1 with 2+ | Auto-pairing alone takes pairing to 23/24 |
| Kids on more than one device | **0** | No dedupe pass needed before the new unique indexes |
| MACs with data but no `ai_device` row | **0** | Backfill attributes 100% of rows |
| Devices whose history predates their current kid | **0** | Backfill is provably lossless; the co-mingling worry is moot |
| Volume on unpaired devices | 13 quiz answers, 104 artifacts, 464 memory docs, 451 voice sessions | Backfill runs in seconds |
| Scale | 24 devices, 11 kids, 152 artifacts, 796 memory docs | Non-event operationally |

**Analytics liveness.** Seven tables are live: `device_analytics_event` (5,625),
`rfid_card_tap_log` (616), `device_games_played` (492), `device_radio_played` (122),
`device_usage_daily` (95), `device_ai_interactions_daily` (79),
`device_card_taps_daily` (40). Eight have zero rows and zero lifetime inserts:
every `analytics_*` table, `game_session`, `kid_activity_log`,
`ai_agent_chat_history`.

**Absent from the database entirely** despite being in `schema.prisma`:
`device_memories`, `memory_chunks`, `user_question_quota`, `custom_content_pack`.

⚠️ This is the dev box. If production has a device that **has** changed hands, the
zero-result queries need re-running there before the backfill.

## 5. Out of scope

**Custom cards.** `rfid_content_pack` codes are `CUSTOM_<MAC>`
(`helpers.js:211`), so a sibling's card plays the previous child's recordings. The
kid-keyed replacement `custom_content_pack` is in `schema.prisma` but its migration
has never been applied, so this is a create rather than an adopt. With **one**
`CUSTOM_` pack on the whole dev box, it is not worth carrying in this phase. The
device-side audio cache needs no version bump when it is done: the toy refetches
when the content hash moves, and a different child's pack is different content.

**Billing and subscription.** Deliberate, see decisions above.

**Voice identification, per-child RFID cards, an active-kid switcher.** All were
considered and rejected: one child uses the toy at a time, so nothing needs to
identify the speaker mid-session.

## 6. Slices

| # | Slice | Blocked by |
|---|---|---|
| 001 | Every device pairs to a child | — |
| 002 | Quiz and Riddler progress follows the child | 001 |
| 003 | Memory and workspace follow the child | 001 |
| 004 | The rolling summary belongs to the child | 003 |
| 005 | The worker's workspace is per child | 001 |
| 006 | Chat history attributes to the child | 001 |
| 007 | Imagine gallery belongs to the child | 001 |
| 008 | Analytics rollups follow the child | 001 |
| 009 | Delete what the survey proved dead | — |
| 010 | Dev box promotion | 002–008 |
