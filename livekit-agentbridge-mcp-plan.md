# Add MCP Support to PicoClaw LiveKit AgentBridge

## Summary

PicoClaw core already supports MCP through `AgentLoop`, but the LiveKit voice path uses `AgentBridge`, which does not initialize MCP. Add MCP to `AgentBridge` without turning it into the full `AgentLoop`: LiveKit should gain MCP tools, discovery behavior, and cleanup while staying lightweight for real-time voice.

## Key Changes

- Extract current MCP setup from `AgentLoop.ensureMCPInitialized` into a shared helper in `pkg/agent`.
- The helper should register MCP tools onto provided `AgentInstance` objects and preserve existing config behavior:
  - no-op when MCP is disabled, no servers exist, or all servers are disabled
  - load servers through `pkg/mcp.Manager`
  - wrap tools with `tools.NewMCPTool(...)`
  - honor global discovery and per-server `deferred`
  - register BM25/regex discovery tools when enabled
- Update `AgentLoop` to use the shared helper so normal PicoClaw behavior remains unchanged.
- Update `cmd/picoclaw-livekit` bridge creation to call the shared MCP helper after LiveKit registers shared/workspace tools, using the actual device workspace.
- Add MCP lifecycle ownership to `AgentBridge`, so `AgentBridge.Close()` closes the MCP manager and avoids leaked stdio processes, HTTP sessions, or MCP connections.

## Out Of Scope

- Do not port the full `AgentLoop` into LiveKit.
- Do not add full multi-agent routing, hook lifecycle, channel bus behavior, or steering queue in this change.
- Do not change the existing `tools.mcp` config schema.

## Test Plan

- Add tests proving LiveKit receives MCP tools when `tools.mcp.enabled` and at least one server/tool is available.
- Add tests for deferred MCP discovery behavior in the shared helper.
- Add a LiveKit lifecycle test proving `AgentBridge.Close()` closes MCP resources.
- Run:
  - `go test ./pkg/agent ./pkg/mcp ./pkg/tools ./pkg/livekit ./cmd/picoclaw-livekit -count=1`
  - On Windows, if `ten_vad.dll` loading fails, rerun with `D:\picoclaw;C:\msys64\mingw64\bin` prepended to `PATH`.

## Assumptions

- LiveKit voice calls should support MCP parity with normal PicoClaw for configured MCP servers.
- MCP startup failures should behave like the current core implementation: log and continue without MCP tools unless the config is structurally invalid.
- This plan is saved at repo root as `livekit-agentbridge-mcp-plan.md`.
