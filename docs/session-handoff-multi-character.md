# Session Handoff — Multi-Character Voice Agents (card picks language) + safe rapid switching

Date: 2026-06-24 · Branch: `multiple_character` (both repos)

## What was built
A device can run **multiple characters**, each in **any language chosen by the RFID card**, on the
**single persona-agnostic `cheeko-agent` worker** — and **rapid card swaps take over instantly** with
no data loss. Three repos:

- **Worker (Go):** `D:\picoclaw` (`cmd/picoclaw-livekit`)
- **Manager API (Node/Prisma):** `D:\cheeko-backend\main\manager-api-node`
- **MQTT Gateway (Node):** `D:\cheeko-backend\main\mqtt-gateway`

## End-to-end flow (card tap)
```
Card { agent_name:"Tenali", language_code:"ta" } tapped
  → Gateway POST /toy/agent/device/:mac/set-character { characterName:"Tenali", language:"Tamil" }
  → Manager: find/create the user's "Tenali" ai_agent from the Tenali template, persist language,
     return contract { characterId, characterName, runtimeAgentName:"cheeko-agent", language:"Tamil",
     systemPrompt, soul }  (persona sourced from ai_agent_template = single source of truth)
  → Gateway dispatches a LiveKit job to runtimeAgentName with metadata { character_id, language, ... }
  → Worker pulls persona by characterId (GET /toy/agent/character/:id/session), renders:
        AGENT.md = persona-agnostic scaffold with <!-- PERSONA --> = systemPrompt
                   and <!-- LANGUAGE --> = "Tamil"
        SOUL.md  = soul
     STT/STT follow the session language (metadata.language → sessionLanguagePolicy)
```
Two independent knobs: **`runtime_agent_name`** = which worker pool (all NULL → `cheeko-agent`);
**`character_id`** = which persona the (persona-agnostic) worker renders.

## Key design decisions
- **Persona = single source in `ai_agent_template`.** `getCharacterSession` / `getCurrentCharacter` /
  `set-character` / `cycle-character` all resolve persona via one `mergeTemplatePersona(agent)` helper
  (template by `agent_name`, falling back to the instance). Edit one template row → changes everywhere.
- **Model B (one character, card picks language).** Personas are language-neutral; the scaffold's
  `<!-- LANGUAGE -->` slot is filled per session with the card/character language. Precedence:
  card language > character/template language > English default.
- **AGENT.md / SOUL.md are session-regenerated, never synced** (ADR-0003). Excluded from workspace-sync
  download **and** upload; USER.md / MEMORY.md still restore.
- **No hashing** (ADR-0001) — worker regenerates AGENT.md/SOUL.md every session from the Manager persona.
- **Last-tap-wins lock preemption.** A new dispatch preempts the per-device distributed workspace lock
  (bumps `fencing_token`); the old session is fenced out, tears down, persists chat history but skips
  the workspace upload and the lock release. No 30s stall, no cross-session corruption.

## Commits (branch `multiple_character`)
**Worker — `D:\picoclaw`:**
- `34ceea7` Phase 3: persona-agnostic worker, PULL persona by characterId
- `4f307b8` vendor ten-vad native libs (fixes the cgo `-lten_vad` build break)
- `7c7201e` fix `go test` cgo build (MSYS2/MinGW) + Phase-3 seed regression; adds `make test-livekit`
- `03fb1f4` persona-pull endpoint is under `/agent` (was 404)
- `d9e5398` wire character/card language into session language policy
- `cdee5c4` language-neutral scaffold with `<!-- LANGUAGE -->` slot
- `90e883e` workspace-sync must NOT restore/upload AGENT.md/SOUL.md
- `0dc40f8` make the ACTIVE scaffold (`workspace/AGENT.md`) persona-agnostic (PERSONA + LANGUAGE slots)
- `42eaaa5` `identity_rendered` log reflects persona path; align `workspace-template/AGENT.md`
- `1c1a2f1` last-tap-wins workspace lock preemption (worker side)

**Manager + Gateway — `D:\cheeko-backend`:**
- `7a96b520` Phase 1: Character→Runtime Agent resolver + worker persona-pull; schema migration
  (`runtime_agent_name` + `soul` on `ai_agent` and `ai_agent_template`)
- `0e95a7cc` docs: Phase 1 pending items
- `66b983a2` Phase 2 gateway: route via Manager `runtimeAgentName`, drop `CHARACTER_AGENT_MAP`
- `59b6f0a9` persona sourced from `ai_agent_template` (single source)
- `c064cba1` single `mergeTemplatePersona` helper for all contract paths
- `3178e2d8` set/cycle-character return full contract + accept language
- `63c73e37` / `39994350` gateway passes card language into the character switch
- `ecdbe941` preempt acquire + fencing rejection (409 `LOCK_PREEMPTED`) for the workspace lock
- `dc35e958` fix workspace-lock acquire 08P01 (bind args must match `$N` on the preempt path) + guard

## Build / test / run
- Worker build: `go build -o ./bin/picoclaw-livekit.exe ./cmd/picoclaw-livekit` (from `D:\picoclaw`).
  Native lib `third_party/ten-vad/lib/Windows/x64/{ten_vad.dll,ten_vad.lib}` is vendored + tracked.
- Worker tests: **`make test-livekit`** (or `sh scripts/test-livekit.sh`). A plain `go test` fails with a
  cgo DLL error on Windows (MSYS2 `msys-2.0.dll` shadows MinGW) — the script fixes PATH.
- Manager tests: `npx jest tests/unit` (from `manager-api-node`) — **278 pass**.
- Run worker: `.\bin\picoclaw-livekit.exe -agent-name cheeko-agent -config "C:\Users\rahul\.picoclaw\config.json" -log-level info`
- **The active scaffold the worker reads is `C:\Users\rahul\.picoclaw\workspace\AGENT.md`** (from config
  `Agents.Defaults.Workspace`), NOT `workspace-template/`. Keep them in sync.

## DB state (seeded this session — production Supabase)
- `ai_agent` + `ai_agent_template` Cheeko: language-neutral persona (systemPrompt + soul).
- `ai_agent_template` rows created (all `runtime_agent_name = NULL` → `cheeko-agent`, neutral personas):
  - **Tenali** `3bf84ff1` · **Bheem** `238c0f68` · **Gattu** `15f5c57b`
- Demo AI cards (`rfid_card_mapping.action_data`):
  - `3DA83C7E` → Tenali / Tamil (`ta`)
  - `5C42C905` → Bheem / Hindi (`hi`)
  - `A4A5CE05` → Gattu / Kannada (`kn`)
- ⚠️ The Cheeko `ai_agent` (`c4765e0c`) + template previously held large prompts (9347 / 20487 chars)
  that were **overwritten** without backup. Restore from Supabase PITR if anything was needed.

## Verification (live worker + Manager, single mimic dispatch — no ESP32)
- 4 characters × 4 languages render correctly: Cheeko/English, Tenali/Tamil, Bheem/Hindi, Gattu/Kannada
  (verified by reading the rendered `workspace-device-28562f07ccdc/AGENT.md`).
- Sync no longer clobbers (`skipped session-regenerated core file restore AGENT.md/SOUL.md`, upload `files=7`).
- **Preemption verified live:** rapid Tenali→Bheem (~1s apart) → token `1→2`, holder flips to room B
  instantly, old room logs `Preempted close: skipping workspace upload` + `Skipping … lock release`,
  final AGENT.md = Bheem/Hindi. No 30s stall, no 500s.

## How to test a tap without a device
There is no committed test harness; the mimic used a throwaway Node script in the gateway repo that:
(1) POSTs `set-character`, (2) builds metadata via gateway `core/mem0-integration.js` `buildDispatchMetadata`,
(3) `RoomServiceClient.createRoom` + `AgentDispatchClient.createDispatch(room, runtimeAgentName, {metadata})`
using LiveKit `http://localhost:7880` (key `devkey`, secret from `mqtt-gateway/config/mqtt.json`).
The rendered prompt lands in `C:\Users\rahul\.picoclaw\workspace-device-<mac>\AGENT.md`. Lock state:
`GET /toy/agent/device/<mac>/workspace-lock`.

## Pending / follow-ups
- **Real-device audio not yet verified** — confirm Sarvam STT + ElevenLabs `eleven_multilingual_v2`
  actually speak Tamil/Hindi/Kannada on a physical tap.
- **RFID card → characterId at the rfid layer is still deferred** (`lookupCardByUid` has no user scope);
  the working path routes card switches through `set-character` instead. See `manager-api-node/docs/PHASE1-PENDING.md`.
- **Migration smoke test** for `20260623000100_add_runtime_agent_and_soul` not run against a real DB
  (idempotent `ADD COLUMN IF NOT EXISTS`; schema validates). See PHASE1-PENDING.
- **Worker file logging:** `PICOCLAW_LOG_FILE` is unset → no on-disk worker log (debugging used the lock DB).
  Set it to capture timelines.
- **Rotate the Supabase DB password** — it was pasted in chat during this session.
- `workspace-template/AGENT.md` and `workspace/AGENT.md` are aligned now; keep them in sync on future edits.
- Multi-pod note: preemption/fencing assumes each session/pod has its own on-disk workspace dir; the
  fencing token protects the shared DB state. True for the normal topology.

## Branch/PR
All work is on `multiple_character` in both repos, committed but **not pushed / no PR** (per process).
Rollout order if deploying: Manager (schema migrate + restart) → Gateway → Worker.
