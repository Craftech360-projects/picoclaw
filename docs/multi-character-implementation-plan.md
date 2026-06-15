# Multi-Character Implementation Plan

Implementation plan for supporting multiple Characters across Manager API, MQTT Gateway, and the picoclaw-livekit worker.

**Design source of truth:** `CONTEXT.md`, `docs/adr/0001-regenerate-agent-md-from-manager-and-template.md`, `docs/adr/0002-manager-owns-character-to-runtime-agent-routing.md`. Read those first — this plan only sequences the work.

## Goal

- Add a new **Persona-only Character** with **one `ai_agent_template` row** — zero gateway changes, zero worker changes, zero deploy.
- Add a **Specialized Character** (own tools/game loop) as a deliberate new-worker + deploy.
- Move Character → Runtime Agent Version routing out of the gateway's hardcoded `CHARACTER_AGENT_MAP` into Manager API.
- Make `AGENT.md` = shared PicoClaw scaffold + Manager-supplied persona (`system_prompt` + `soul`), with child data confined to `USER.md`.

## The resolved session-resolution contract

Both selection paths (default-by-MAC `current-character`, and AI-card-by-uid) must return the same shape:

```jsonc
{
  "characterId": "uuid",
  "characterName": "Cheeko German",
  "runtimeAgentName": "cheeko-agent1",   // resolved: NULL char column -> Manager default
  "language": "de",
  "systemPrompt": "…persona for AGENT.md block…",
  "soul": "…persona for SOUL.md…",
  "promptHash": "sha256(systemPrompt + soul)",   // Manager-computed
  "templateHash": "sha256(active scaffold version)" // Manager-computed
}
```

The worker composes the local regeneration key `hash(characterId + promptHash + templateHash)`.

---

## Phase 1 — Manager API (`D:\cheeko-backend\main\manager-api-node`)

**1.1 Schema** (`prisma/schema.prisma`)
- Add nullable `runtime_agent_name VARCHAR` to `ai_agent` (and `ai_agent_template` so templates can seed it).
- Add `soul TEXT` to `ai_agent` / `ai_agent_template` (persona second surface). `system_prompt` already exists.
- Migration: backfill existing rows — persona-only stay `NULL` (resolve to default), specialized get their explicit agent name (`math-tutor-agent`, `riddle-solver-agent`, `word-ladder-agent`).

**1.2 Config**
- Add `LIVEKIT_DEFAULT_AGENT` env (default `cheeko-agent1`). This is the **Default Runtime Agent** version — the one place to bump for rollout/canary.

**1.3 Shared resolver** (new `livekitSessionResolver.service.js`, or in `agent.service.js`)
- `resolveRuntimeAgentName(character)` → `character.runtime_agent_name ?? process.env.LIVEKIT_DEFAULT_AGENT`.
- `resolveSessionForCharacter(character, { language })` → builds the contract object above, including `promptHash`/`templateHash`.
- `templateHash` = hash of the active scaffold version (see Phase 3.1 — Manager must know the scaffold version; simplest: a `scaffold_version` constant/table both sides agree on, or Manager stores the canonical scaffold).

**1.4 Wire both paths through the resolver**
- `getCurrentCharacter` (`agent.service.js:1512`) → return full contract, not just `characterName`.
- AI-card lookup (`rfid.service.js:914`) → after resolving `agentName`(=character)+`languageCode`, look up the Character and run the same resolver so the card path returns `runtimeAgentName` + persona + hashes too.
- Keep response back-compatible: add new fields, don't remove `characterName`.

**Verification:** unit test resolver (NULL→default, explicit→explicit); contract test both endpoints return identical shape; migration smoke test.

---

## Phase 2 — MQTT Gateway (`D:\cheeko-backend\main\mqtt-gateway`)

**2.1 Delete `CHARACTER_AGENT_MAP`** (`gateway/mqtt-gateway.js:36`).

**2.2 Migrate the 4 dispatch sites** (`:2203`, `:2434`, `:2972`, `:3460`):
- Replace `const agentName = CHARACTER_AGENT_MAP[characterName] || CHARACTER_AGENT_MAP["Cheeko"]` with `const agentName = resolution.runtimeAgentName`.
- The `current-character` call (`:2185`, `:2413`) now returns `runtimeAgentName` directly — use it; drop the separate name→agent translation.
- For the RFID/character-change path (`:2945`), use the card resolution's `runtimeAgentName`.

**2.3 Fallback** — keep a single constant `DEFAULT_RUNTIME_AGENT = 'cheeko-agent1'` used **only** when the Manager call fails/times out. No per-character map.

**2.4 Dispatch metadata** — pass `language`, child profile, and `characterId` in `buildDispatchMetadata` so the worker has them without an extra round-trip.

**Verification:** dispatch logs show `runtimeAgentName` from Manager; persona-only characters all dispatch to `cheeko-agent1`; specialized to their own agent; Manager-down path falls back to the constant.

---

## Phase 3 — Worker (`d:\picoclaw`)

**3.1 Define the scaffold** — `workspace-template/AGENT.md` becomes **persona-agnostic**: remove the Cheeko-specific Core Identity/Vibe; insert a persona placeholder (e.g. `<!-- PERSONA -->`). All shared sections (Capabilities, Safety, Runtime Guardrails, Memory, Storytelling, Age Adaptation, Search/Time, Truthfulness) stay. `templateHash` = hash of this scaffold version.

**3.2 Delete `prompts/cheeko.tmpl`** AND remove the render block at `cmd/picoclaw-livekit/main.go:526-554` in the **same commit** (the worker reads the file there; deleting alone breaks the session).

**3.3 Build `AGENT.md` from scaffold + Manager persona**
- Load scaffold → replace persona placeholder with Manager `systemPrompt`. Write `AGENT.md`.
- Write `SOUL.md` from Manager `soul`. **(Flag #2 — both persona surfaces regenerate together.)**
- Child profile → `USER.md` only (the `.ChildProfile` template already exists there). Never into `AGENT.md`.

**3.4 Regeneration gate** (the 7-step plan)
1. Restore workspace from DB.
2. Read sidecar `state/agent_md.meta.json` → previous `key`.
3. Compute current `key = hash(characterId + promptHash + templateHash)` (hashes from Manager contract).
4. If `key` changed (or sidecar missing) → regenerate `AGENT.md` + `SOUL.md`, write new sidecar `{ key, generatedAt }`.
5. Run session.
6. On close: persist `USER.md` + `memory/MEMORY.md` (per-Device, unchanged across switches).
7. Cache `AGENT.md` but never trust it over Manager (ADR-0001).

**3.5 Specialized workers** — unaffected: they ship their own prompt and do **not** run 3.1–3.4. Manager just routes to them; shared context arrives via dispatch metadata.

**Verification:** switch Cheeko→Cheeko German→Math Tutor on one device — `AGENT.md` + `SOUL.md` swap, no "I am Cheeko" bleed, all PicoClaw feature sections (safety/memory/skills) intact; two children on same Character produce correct per-child `USER.md` while `AGENT.md` is identical; sidecar key skips regeneration when nothing changed.

---

## Phase 4 — USER.md refresh (decide before building)

`USER.md` is currently written only at workspace creation, so child-profile updates don't propagate. Decide: (a) leave as-is, or (b) re-render `USER.md` from bootstrap when `childProfileHash` changes (note: child is **not** in the AGENT.md key, so this needs its own small check). Recommend (b) — cheap and avoids stale profiles.

---

## Rollout & backward compatibility

1. **Phase 1 first**, additive (new fields/columns, default resolves NULL). Old gateway ignores new fields — safe.
2. **Phase 2** once Manager returns `runtimeAgentName`. Behavior identical for existing characters (they resolve to the same agents), so low risk.
3. **Phase 3** is worker-internal; gate behind the scaffold being persona-agnostic. Deploy the Default Runtime Agent worker; persona-only characters now serve from Manager persona.
4. Seed `soul`/`system_prompt` for existing characters before flipping Phase 3, or the persona block is empty.

## Risks / watch-items

- **Empty persona:** if `system_prompt`/`soul` not seeded, `AGENT.md`/`SOUL.md` lose persona. Backfill before Phase 3.
- **templateHash agreement:** Manager and worker must agree on scaffold version. Pin a `scaffold_version` both reference, or have Manager serve the scaffold.
- **Hash must cover everything rendered into AGENT.md** — keep child/language out, or the key lies (the whole reason for deleting `cheeko.tmpl`).
- **4 gateway sites, not 1** — miss one and that path still hard-codes routing.
```
