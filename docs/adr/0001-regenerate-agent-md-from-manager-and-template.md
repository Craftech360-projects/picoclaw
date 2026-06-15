# Regenerate AGENT.md From Manager and Template

For device voice sessions, `AGENT.md` is a generated runtime prompt, not the source of truth for a Character. The selected Character Prompt comes from Manager API, shared safety/runtime rules come from the workspace template, and Picoclaw regenerates `AGENT.md` at session start when the character, character prompt, or template changes. Room close should persist durable user and memory state, but the next session must not rely on a previously uploaded `AGENT.md` to determine the active Character.

## Persona has two surfaces, not one

The Character persona is **not** confined to `AGENT.md`. `SOUL.md` is also fully persona (`"I am Cheeko…"`, voice flavor, identity guard), and `AGENT.md` instructs the agent to read it. Therefore the Manager Character Prompt is stored as **two fields — `system_prompt` and `soul`** — and the worker regenerates **both** `AGENT.md`'s persona block and `SOUL.md` on a Character switch. Regenerating only `AGENT.md` causes persona bleed (e.g. switching to Cheeko German while `SOUL.md` still says "I am Cheeko").

## Workspace file taxonomy

- **Per-Character (regenerate from Manager, keyed by `promptHash`):** the persona block in `AGENT.md`, and `SOUL.md`.
- **Shared scaffold / PicoClaw features (preserve, keyed by `templateHash`):** the non-persona sections of `AGENT.md`, `HEARTBEAT.md`, `memory/MEMORY.md` structure, `skills/`, `time/`. These must survive Character switches — switching persona must never drop a PicoClaw feature.
- **Per-Device state (preserve across switches):** child profile in `USER.md` (already templated from `.ChildProfile` — child data must land here, never baked into `AGENT.md`), and memory content in `memory/MEMORY.md`.

## Regeneration key

`hash(characterId + promptHash + templateHash)`, persona-only with respect to child data. `templateHash` versions the shared scaffold so a feature change forces regeneration; `promptHash` covers both persona fields (`system_prompt` + `soul`). Language and child profile are **not** in `AGENT.md` (language → runtime/provider config; child → `USER.md`). The worker writes a sidecar meta (`{ key, generatedAt }`) next to `AGENT.md` so the compare is local; Manager computes `promptHash`/`templateHash` authoritatively.
