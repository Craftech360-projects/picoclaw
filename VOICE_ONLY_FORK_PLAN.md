# PicoClaw Voice-Only Fork Plan

## Status
- Drafted before implementation.
- Scope and decisions are based on the alignment session.

## Goal
- Build a voice-only LiveKit runtime branch for kid interactions.
- Keep configuration flexibility via Manager API with config fallback.
- Reduce runtime risk by minimizing tools and non-voice surfaces.

## Branch
- Target branch: `codex/voice-only-livekit`
- Main branch remains untouched until Phase 1 verification passes.

## Architecture Decisions (Locked)
- Hard fork on a new branch inside the same repository.
- Hybrid config model:
  - Primary: Manager API provider config
  - Fallback: `config.json`
- Global provider settings (common for all devices).
- Provider schema style: sibling tables
  - `stt_providers`
  - `llm_providers`
  - `tts_providers` (includes `voice_id`)
- Provider updates apply to new room sessions only.
- Manager API returns plaintext keys to worker (current-style).
- Soft fallback to `config.json` if Manager API fails.
- Local memory updates during session.
- Manager API memory sync only on room close.

## Tool Strategy (Locked)
- Keep native:
  - `web_search`
  - `web_fetch`
  - `read_file`
  - `write_file`
  - `list_dir`
- Add native:
  - `get_time_date`
  - `get_weather`
- Remove from voice runtime:
  - `exec`
- Guidance skills:
  - Keep weather skill, but update it to use `get_weather` first
  - Add time skill and auto-enable it by default
- Defense in depth:
  - Explicit allowlist registration
  - Runtime guard blocking any non-allowlisted tool

## What We Are Removing
### Phase 2 hard deletions
- `web/` directory (frontend + launcher backend)
- Launcher-related runtime/code paths
- `cmd/picoclaw` binary entrypoint
- Non-voice channel adapters and non-voice command surfaces
- Non-voice docs/build wiring that no longer applies

### Kept entrypoint
- `cmd/picoclaw-livekit` only

## What We Are Restricting
- Restrict voice runtime tool surface to:
  - `web_search`, `web_fetch`, `get_weather`, `get_time_date`, `read_file`, `write_file`, `list_dir`
- Disallow `exec` in voice runtime.
- Narrow CI to voice-relevant paths only:
  - `cmd/picoclaw-livekit`
  - `pkg/livekit`
  - `pkg/voice`
  - required parts of `pkg/agent`
  - required parts of `pkg/tools`

## Phase 1: Runtime and Behavior
1. Create branch `codex/voice-only-livekit`.
2. Add native `get_time_date` tool.
3. Add native `get_weather` tool.
4. Add `skills/time/SKILL.md` and auto-enable it by default in LiveKit voice sessions.
5. Update `skills/weather/SKILL.md` to prioritize `get_weather`.
6. Enforce explicit tool allowlist registration for voice runtime.
7. Add runtime guard to block non-allowlisted tools.
8. Remove `exec` from voice runtime path.
9. Add Manager API provider resolver endpoint support:
   - `GET /livekit/providers/active`
   - payload: `{ llm, stt, tts, updated_at }`
10. Add provider cache (worker-side TTL: 30s).
11. Resolve providers from Manager API first, fallback to `config.json`.
12. Keep provider changes effective only for new room sessions.

### Phase 1 Verification
- Time answers are deterministic and timezone-correct from `USER.md`, fallback `Asia/Kolkata`.
- Weather answers use native `get_weather`.
- Allowed tools work; blocked tools are rejected by runtime guard.
- STT/LLM/TTS resolve from Manager API when available and from fallback when unavailable.
- Existing LiveKit room flow remains stable.

## Phase 2: Hard Fork Cleanup
1. Delete `web/` and launcher-related code paths.
2. Remove `cmd/picoclaw`; keep `cmd/picoclaw-livekit` only.
3. Remove non-voice channel/CLI surfaces.
4. Trim build scripts and docs to voice-only scope.
5. Update CI to voice-relevant targets only.

### Phase 2 Verification
- Voice-only build succeeds.
- Voice-focused tests pass in narrowed CI.
- No frontend/launcher/non-voice runtime coupling remains.

## Migration and Deployment Order
1. Apply DB migrations first (required).
2. Deploy Manager API endpoint for active providers.
3. Deploy Phase 1 worker changes.
4. Validate runtime behavior in staging.
5. Execute Phase 2 cleanup.

## Risks and Mitigations
- Risk: Manager API outage.
  - Mitigation: soft fallback to `config.json`.
- Risk: Tool behavior drift after removing `exec`.
  - Mitigation: native `get_weather` and `get_time_date` before `exec` removal.
- Risk: Re-coupling with removed surfaces.
  - Mitigation: hard deletion plus runtime guard plus narrowed CI.

## Deliverable Commits
- Commit A (Phase 1): tools/runtime/provider resolution + tests.
- Commit B (Phase 2): hard deletions + CI/doc cleanup + tests.

