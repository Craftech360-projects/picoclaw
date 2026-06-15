# Manager Owns Character → Runtime Agent Version Routing

The MQTT Gateway no longer hardcodes the `CHARACTER_AGENT_MAP` that translated a Character name into a LiveKit Runtime Agent Version. Instead, Manager API resolves the Runtime Agent Version (the Runtime Routing Policy) and returns `runtimeAgentName` on both session-resolution paths — default-by-MAC (`current-character`) and AI-card-by-uid — and the gateway dispatches to whatever it receives.

## Context

The word "agent name" collides: `ai_agent.agent_name` is the **Character** display name (`"Cheeko"`), while LiveKit's `agent_name` is the **Runtime Agent Version** (`"cheeko-agent1"`). The gateway's hardcoded map existed only to bridge these two, which meant every new Character required a gateway code change and redeploy.

## Decision

- **Persona-only Characters** (Cheeko, Cheeko German, Cheeko Astronaut, Cheeko Magic, and all future template-created Characters) all resolve to the **Default Runtime Agent**. A nullable `runtime_agent_name` column on the Character is left `NULL`; Manager resolves `NULL` to a single configured default (`cheeko-agent1`).
- **Specialized Characters** (Math Tutor, Riddle Solver, Word Ladder) set `runtime_agent_name` explicitly and ship as their own self-contained workers.
- A **single shared Manager resolver** populates `runtimeAgentName` for both the `current-character` and AI-card paths, so they cannot drift.
- The gateway keeps **one** last-resort constant only for "Manager unreachable" — never a per-Character map.

## Consequences

- Adding a Persona-only Character = one `ai_agent_template` row, zero gateway/Manager code changes.
- Adding a Specialized Character = one row with `runtime_agent_name` set + deploying that worker.
- Runtime Agent Version bumps (rollout/canary/rollback) become a Manager-side config change, not a gateway deploy.
- The gateway's four `CHARACTER_AGENT_MAP[...]` dispatch sites must be migrated to use `runtimeAgentName` from the Manager response.
