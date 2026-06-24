# Multi-Character via RFID Cards — proper flow (characters + languages)

Goal: tapping an AI card switches the device to the card's character **and** language, and the
worker speaks that character's persona in that language — for ANY character, not just Cheeko.

## Key design decision: one worker, many characters

Characters differ by **persona (template) + language**, NOT by worker. All run on the single
persona-agnostic `cheeko-agent` worker (`runtime_agent_name = NULL` → default). No new worker
deployments are needed to add a character. (Specialized workers with a different LLM/voice stay
a future option via a non-null `runtime_agent_name`, but aren't required here.)

## Example characters (templates)

| Character     | Language | lang_code | runtime_agent_name | Persona |
|---------------|----------|-----------|--------------------|---------|
| Cheeko        | English  | en        | NULL (cheeko-agent)| playful best friend (default) |
| Tenali        | Tamil    | ta        | NULL (cheeko-agent)| witty Tamil storyteller |
| Bruno         | German   | de        | NULL (cheeko-agent)| adventurous explorer bear |

Each is one `ai_agent_template` row (persona = `system_prompt` + `soul`, single source).
Cards reference a character by name + language in `action_data`:
`{ "agent_name": "Tenali", "language_code": "ta" }`.

## The flow (card tap)

```
Card tap (action_data: agent_name=Tenali, language_code=ta)
  → Gateway: POST /agent/device/:mac/set-character { characterName: "Tenali", language: "ta" }
  → Manager setCharacterByName: find/create the user's "Tenali" ai_agent from the Tenali
      template (copies persona/config), set device.agent_id, set language=ta,
      RETURN full contract { characterId, characterName, runtimeAgentName, language, ... }
  → Gateway: dispatch to runtimeAgentName (cheeko-agent) with metadata
      { character_id, language, ... }
  → Worker: pull persona by character_id (template-sourced) → render AGENT.md/SOUL.md;
      language drives STT/TTS
```

This reuses the existing character-switch machinery (the gateway already calls set-character on a
card that maps to a different agent), and the worker already pulls persona by `character_id`.

## Changes by layer

### Manager (`manager-api-node`) — KEYSTONE (this is what unblocks everything)
- `setCharacterByName(mac, characterName, { language })`: accept optional language; set it on the
  agent; **return the full contract** (characterId, characterName, runtimeAgentName, language,
  systemPrompt, soul) via `mergeTemplatePersona` + `resolveSessionForCharacter` — not just
  `{ agentId, agentName }`. (Closes PHASE2-PENDING #1.)
- `cycleCharacter(mac)`: same — return the full contract.
- Routes `POST /device/:mac/set-character` (read `language` from body) and `/cycle-character`:
  return `{ success, newModeName, characterId, runtimeAgentName, language }`.

### Gateway (`mqtt-gateway`)
- AI-card path: pass the card's `language_code` into set-character; on character switch use the
  returned `characterId` + `runtimeAgentName` + `language` for dispatch + metadata. The
  CHARACTER-CHANGE dispatch site already destructures these from the response — once the Manager
  returns them, it works.

### Worker (`picoclaw`)
- Wire per-session `language` (from metadata) into `sessionLanguagePolicy` so STT/TTS follow the
  character/card language when no explicit card session-language is set. (PHASE3-PENDING #1.)

### Seed (DB)
- One `ai_agent_template` per character (Cheeko/Tenali/Bruno) with persona + lang_code.
- AI cards with `action_data = { agent_name, language_code }` pointing at them.

## Status
- [x] Manager keystone (setCharacterByName/cycleCharacter return contract + language) + routes
- [ ] Gateway: pass card language into set-character
- [ ] Worker: metadata.language → sessionLanguagePolicy
- [ ] Seed example characters + cards
