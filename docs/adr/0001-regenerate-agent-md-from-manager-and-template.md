# Regenerate AGENT.md From Manager and Template

For device voice sessions, `AGENT.md` is a generated runtime prompt, not the source of truth for a Character. The selected Character Prompt comes from Manager API, shared safety/runtime rules come from the workspace template, and Picoclaw regenerates `AGENT.md` at session start when the character, character prompt, or template changes. Room close should persist durable user and memory state, but the next session must not rely on a previously uploaded `AGENT.md` to determine the active Character.
