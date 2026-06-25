# Plan — Full AGENT.md in the DB (admin-editable, language-only injection)

Goal: make each character's **entire** AGENT.md live in the database (single source, admin-editable),
with the worker only filling the `<!-- LANGUAGE -->` slot at session start. Removes the worker's
dependency on the on-disk `workspace/AGENT.md` scaffold (the stale-scaffold class of bug). SOUL.md is
already fully DB-stored, so this makes AGENT.md symmetric with it.

## Design
- Store the full AGENT.md (scaffold + persona, with exactly one `<!-- LANGUAGE -->` placeholder) per
  character, sourced from `ai_agent_template` (single source), instance override allowed.
- Worker pulls it via the existing `GET /agent/character/:id/session`, fills `<!-- LANGUAGE -->`, writes
  AGENT.md. No `<!-- PERSONA -->` merge, no scaffold file read.
- Backward compatible: if a character has no stored `agent_md`, the worker falls back to the CURRENT
  behavior (scaffold + `system_prompt`), so deploy is safe and backfill can be gradual.
- Safety preserved by a **save-time validator** (full prompt must contain the required safety sections
  + exactly one language slot), since the scaffold no longer guarantees inheritance.

## 1. Schema (Manager / Prisma)
- Add nullable `agent_md TEXT` to **`ai_agent`** AND **`ai_agent_template`** (idempotent migration,
  `ADD COLUMN IF NOT EXISTS`, like the `runtime_agent_name`/`soul` migration).
- Keep `system_prompt` (persona-only) — used by the re-compose script (see §6) and the fallback path.

## 2. Manager — resolver + contract
- `mergeTemplatePersona(agent)` (agent.service.js): also resolve `agent_md` from the template
  (fallback to instance). Add `agentMd` to the object.
- `resolveSessionForCharacter` (character-resolver.js): add `agentMd: character.agent_md ?? null` to the
  returned contract. (Keep `systemPrompt`/`soul`.)
- `getCharacterSession` / `getCurrentCharacter` selects must include `agent_md`.
- Endpoint `GET /agent/character/:id/session` now returns `{ ..., systemPrompt, soul, agentMd }`.

## 3. Manager — save-time safety validator
- A pure `validateAgentMd(text)` helper that throws 400 unless the text:
  - contains `## Child-Safety Rules` (or the agreed required headings: Child-Safety, Runtime Guardrails),
  - contains exactly one `<!-- LANGUAGE -->`,
  - is non-empty / under a max size.
- Call it in every write path that sets `agent_md`: template create/update (`/agent/template` POST/PUT),
  any character `agent_md` update endpoint, and a new/extended PUT for editing a character's prompt.
- Unit tests: valid passes; missing safety heading rejected; zero/two language slots rejected.

## 4. Worker — pull + inject language only
- `managerCharacterSession` (manager_workspace_bootstrap.go): add `AgentMd string \`json:"agentMd"\``.
- `main.go` bridgeFactory: capture `personaAgentMd = session.AgentMd`; pass it to hydration
  (`hydrationOptions.AgentMdContent`).
- `workspace_hydration.go`:
  - Add `AgentMdContent string` to `liveKitWorkspaceHydrationOptions`.
  - In the AGENT.md write: if `RegeneratePersona && AgentMdContent != ""` →
    `agentContent = injectLanguage(AgentMdContent, SessionLanguage)` and write (overwrite every session).
    Else → existing path (scaffold read + `injectPersona` + `injectLanguage`) as the fallback.
  - `injectPersona` and the `<!-- PERSONA -->` slot stay only for the fallback/legacy path.
- No change to SOUL.md, USER.md, memory, sync-exclusion, or lock preemption.

## 5. Data backfill (one-off, per character)
- Script (Node, like the earlier seeds): for each `ai_agent_template`, set
  `agent_md = currentScaffold` with `<!-- PERSONA -->` replaced by that row's `system_prompt`
  (keep `<!-- LANGUAGE -->`). `currentScaffold` = the committed persona-agnostic
  `workspace-template/AGENT.md` (or `workspace/AGENT.md`).
- Result: every existing character (Cheeko, Tenali, Bheem, Gattu) gets a full, editable AGENT.md that
  equals what the worker renders today — so behavior is identical, just now DB-driven.
- Run validator on each backfilled value before writing.

## 6. Global rule changes (keep an update-once path)
- Because the full prompt is now per-character, a global safety/voice change is per-row. Mitigate with a
  **re-compose script**: `agent_md = master_scaffold` (a doc or a `default` template row) with each row's
  `system_prompt` re-injected. So: edit the master scaffold once → re-run compose → all characters updated.
- Document this in the ADR; it's the trade-off for full-prompt editability.

## 7. Admin UI (out of scope here, note for frontend)
- The web frontend's character/template editor should edit `agent_md` (a textarea) and surface validator
  errors. Persona-only editing can stay too (writing `system_prompt` + re-compose). Not part of this plan.

## 8. Rollout / backward-compat
1. Manager: migrate (`agent_md` columns) + deploy resolver/validator (returns `agentMd`, null for now).
2. Worker: deploy the agentMd-aware build (uses `agent_md` when present, else falls back — safe with nulls).
3. Backfill `agent_md` for the 4 characters (validator-checked).
4. Verify with a tap: rendered AGENT.md == the stored `agent_md` with language filled; tweak the DB row,
   re-tap, confirm the change shows with no worker change.

## 9. Tests
- Manager: `validateAgentMd` unit tests; resolver returns `agentMd`; getCharacterSession shape includes it.
- Worker: hydration test — when `AgentMdContent` set, AGENT.md == `injectLanguage(agentMd, lang)` and the
  `<!-- PERSONA -->` path is NOT used; fallback test — when empty, old scaffold+persona path still works.

## 10. Decisions for you before coding
- **Column vs reuse:** new `agent_md` column (recommended) vs overload `system_prompt`. Recommend new column
  so `system_prompt` stays the persona-only field for re-compose.
- **Required safety headings:** confirm the exact heading strings the validator enforces
  (proposed: `## Child-Safety Rules` and `## Runtime Guardrails`).
- **Fallback:** keep the scaffold+persona fallback for null `agent_md` (recommended) or hard-require agent_md.

## Effort / risk
- Small, mostly additive. Migration + ~3 worker edits + resolver/validator + a backfill script + tests.
- Lowest-risk because it's backward-compatible (null `agent_md` = today's behavior). The only behavior
  change is for characters whose `agent_md` is populated.
