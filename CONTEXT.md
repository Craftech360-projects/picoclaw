# Picoclaw LiveKit Voice Context

This context defines the product language for device voice sessions that run through Manager API, MQTT Gateway, and LiveKit workers.

## Language

**Character**:
A persona selected for a device voice session; one **Device** has at most one current **Character** at a time. A Character is not a separate LiveKit worker by default.
_Avoid_: Runtime agent, worker, mode

**Persona**:
The prompt, voice, language, and memory style that make a Character feel distinct during conversation.
_Avoid_: Bot process, service

**Character Prompt**:
The persona-specific instructions for a Character, separate from shared kid-safety, runtime, and memory rules.
_Avoid_: Full workspace prompt, runtime guardrails

**Runtime Agent**:
A LiveKit execution capability that runs a voice session. Multiple Characters can use the same Runtime Agent when only persona changes. A Runtime Agent can have multiple Runtime Agent Versions registered in the same LiveKit project.
_Avoid_: Character

**Runtime Family**:
A group of Runtime Agent Versions that can run the same kind of session, for example the `cheeko` family.
_Avoid_: Character family, persona group

**Runtime Agent Version**:
A concrete LiveKit `agent_name` registered by one deployment of a Runtime Agent, for example `cheeko-agent`, `cheeko-agent1`, or `cheeko-agent2`.
_Avoid_: Character, persona

**Runtime Routing Policy**:
The Manager API decision that maps a Device session to a Runtime Agent Version. This supports stable rollout, testing, canary, and rollback without changing the selected Character.
_Avoid_: Character selection

**Default Runtime Agent**:
The shared Runtime Agent family used by persona-only Characters. The concrete Runtime Agent Version is selected by Runtime Routing Policy.
_Avoid_: Character-specific worker

**Device**:
A physical toy identified by MAC address. A Device selects its current Character through Manager API.
_Avoid_: User, child

## Example Dialogue

Developer: "Should Cheeko Magic be a new Runtime Agent?"

Domain expert: "No, it is just a Character. Use the same Runtime Agent unless it needs different tools or a different game loop."

Developer: "So the Device stores the current Character, and Manager provides that Character's persona?"

Domain expert: "Exactly."

Developer: "Do all normal Characters dispatch to the same LiveKit agent name?"

Domain expert: "Yes. Persona-only Characters use the Default Runtime Agent family, and Runtime Routing selects the concrete Runtime Agent Version."

Developer: "Are cheeko-agent, cheeko-agent1, and cheeko-agent2 different Characters?"

Domain expert: "No. They are Runtime Agent Versions. They let us deploy or test different worker versions without changing the Character."

Developer: "When the Character changes, do we restore the old AGENT.md as the source of truth?"

Domain expert: "No. The Character Prompt is the source of truth for persona, and shared rules stay separate."
