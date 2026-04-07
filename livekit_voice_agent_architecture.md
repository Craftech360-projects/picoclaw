# PicoClaw LiveKit Voice Agent Architecture

This document provides a detailed overview of the LiveKit integration branch for the PicoClaw voice agent. It details how the system is configured, the primary data flow, the most critical files, how ephemeral workspaces are managed on a per-user basis, and how memory and disks are cleaned up when users disconnect.

## 1. High-Level Flow
The `picoclaw-livekit` program runs as a long-lived **LiveKit Worker**. 
1. It registers itself with the LiveKit server via WebSockets.
2. The LiveKit server sends an `AvailabilityRequest` when a user requests a room. The worker verifies it has capacity.
3. If it has capacity, it receives a `JobAssignment` (a room assignment).
4. For each assigned room, the system calculates a unique ephemeral workspace, parses the user's specific context (like a child profile) from the room's metadata, creates a temporary workspace folder on disk, dynamically injects personalized prompt files, and then connects an LLM `AgentBridge` to the LiveKit Audio tracks.
5. It spins up Voice Activity Detection (VAD) and a streaming Audio Pipeline (incorporating Deepgram STT, and Cartesia/ElevenLabs/etc for TTS).
6. When the user disconnects, the agent destroys the local LLM processes, drops the conversation context, and deletes the ephemeral workspace from the hard drive automatically.

## 2. Configuration & Initialization
- **Environment:** Config files are loaded heavily relying on standard PicoClaw configurations under `~/.picoclaw/config.json` alongside `.env` variables.
- **Provider Setup:** It loads the chosen LLM provider (Ollama, Gemini, OpenAI) and sets up the TTS provider (Cartesia, Deepgram STT, Inworld, etc.).
- **Worker Limits:** The worker is bounded by a concurrency cap (`maxSessions`, default 100) preventing OOM errors and gracefully rejecting routing assignments it cannot handle.

## 3. Important Files
- **`cmd/picoclaw-livekit/main.go`**: The entry point. Handles parsing config, initializing the TTS factory, determining the worker configurations, and implements the critical `bridgeFactory` where ephemeral workspaces are generated.
- **`pkg/livekit/worker.go`**: The LiveKit Worker dispatch loop. It handles WebSocket connection to the LiveKit server, ping/pong loop, availability scaling, and assigns jobs to RoomSessions.
- **`pkg/livekit/room_session.go`**: The WebRTC integration. Responsible for actually joining a LiveKit Room. Uses TEN VAD to handle speech interruption and streams microphone audio. It also serves as the webhook boundary for the `DataChannel` listening for commands like `end_prompt`.
- **`pkg/livekit/agent_bridge.go`**: The core logic layer wrapping instances of the PicoClaw text-based LLM logic. It handles history summaries, enforces the "Voice Directive" on LLM generation, and natively hooks into completed background tools to trigger "spontaneous generation events" (audio notifications of background tasks).
- **`pkg/livekit/audio_pipeline.go`**: (Not deeply reviewed, but present) Dedicated to ferrying VAD signals, tracking when the user is speaking versus when the LLM is speaking, and passing transcribed sentences to the `AgentBridge`.

## 4. Workspace Management for Each User
The true power of this branch lies in how it dynamically sandboxes every WebRTC voice session so that the LLM natively assumes the contextual persona for *that specific caller*.
- When a room connects, metadata is passed through `job.Room.Metadata` as stringified JSON containing properties such as:
  ```json
  "child_profile": {"name": "Alex", "age": 8, "interests": "space"}
  ```
- **The Ephemeral Folder**: The routing layer generates an arbitrary workspace directory based on the unique agent definition (`job.Room.Name`) inside the `workspace-*` pattern.
- **Dynamic Prompt Rendering**: `main.go` uses Go Templates (`prompts/cheeko.tmpl`) applied against the child profile metadata. It then writes the resulting `IDENTITY.md` file *directly into the new ephemeral workspace folder*.
- Once the file is written, the underlying `agent.NewAgentInstance` is booted up. This instance automatically consumes the workspace and reads `IDENTITY.md`, thereby zero-latency updating the context without relying on extra API round trips.

## 5. Tooling & Asynchronous Tasks
Standard text LLMs pause conversation when calling tools. But for voice, it needs to be asynchronous. 
- In `agent_bridge.go`, tools are triggered in a completely detached Goroutine (`go func(asyncCtx context.Context ...)`). 
- The user can simply keep speaking. Once the background tool completes, the callback fires a signal down an `asyncEventChan`.
- This triggers `GenerateSpontaneousResponse()`, where the system injects a message like `"[Background Task Completed] Tool 'x' finished..."` silently into the history. The LLM then generates a spontaneous voice line ("I've processed that for you!").

## 6. Teardown, Cleanup, & Room Closing
A major vulnerability of worker apps is data leakage and zombie workspaces filling up the disk. This is heavily handled:
- LiveKit provides disconnection states via `cb.OnParticipantDisconnected` and `cb.OnDisconnected`. There is also a manual `shutdown_request` MQTT data channel instruction.
- Any disconnect forces a `RoomSession.Leave()`.
- **Graceful Shutoff:** `Leave()` stops Deepgram STT, stops local audio tracks, writes end-of-session metrics + full transcripts to any analytics tracking endpoints, and then triggers `AgentBridge.Close()`.
- **Scrubbing Disk Space**: Inside `AgentBridge.Close()`, the bridge terminates `AgentInstance` and *explicitly removes the entire workspace sandbox using `os.RemoveAll(ab.agentInstance.Workspace)`*. 
- **Summary**: When a call is done, memory context drops, audio streams are killed, and the entire workspace sandboxed file-system is securely deleted entirely from disk. 
