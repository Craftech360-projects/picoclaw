# LiveKit Voice Agent Standalone Service - Design Spec (Approach C)

## Overview

Build a standalone `picoclaw-livekit` binary that acts as a **LiveKit named agent worker**. It registers with the LiveKit server using the standard agent dispatch protocol (same as Python/Node agent frameworks), receives job assignments when users request the agent, and automatically joins rooms to handle voice conversations. It imports PicoClaw's provider, session, and tool packages directly for AI quality.

## Requirements

- Voice-only LiveKit agent (no video)
- **Named agent auto-dispatch** — identical to how LiveKit Python/Node agents work
- One worker process handles 1000+ rooms concurrently
- Each room: one user, one agent instance, isolated session
- Deepgram streaming STT with built-in endpointing (no separate VAD)
- ElevenLabs streaming TTS for voice output
- Immediate interruption: cancel TTS when user starts speaking
- Direct import of PicoClaw's provider and session packages
- Separate binary: does not modify PicoClaw gateway or channel system

## Important Limitations

AgentBridge builds a **simplified agent execution path** using PicoClaw's provider, session, and tool packages — NOT wrapping `AgentLoop.runTurn()` (which is ~900 lines coupled to MessageBus).

**Features NOT available in Approach C** (present in Approach A):
- Steering messages (mid-turn user interruptions via text)
- Sub-agent spawning (`spawn` tool)
- Mid-turn context compression and summarization
- Tool approval hooks (BeforeTool/AfterTool interceptors)
- Event bus emissions (turn start/end, LLM retries)
- Graceful abort cascades from parent turns

For voice agents, most of these are irrelevant (steering is voice-native via interruption, sub-agents are unlikely in voice). The subset is sufficient for voice use cases.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    picoclaw-livekit binary                    │
│                    (cmd/picoclaw-livekit/)                    │
│                                                               │
│  ┌───────────────────────────────────────────────────────┐   │
│  │              Worker (pkg/livekit/worker.go)            │   │
│  │                                                        │   │
│  │  WebSocket ←──→ LiveKit Server                         │   │
│  │  - RegisterWorkerRequest(agent_name="support-bot")     │   │
│  │  - Receives AvailabilityRequest                        │   │
│  │  - Responds AvailabilityResponse(accept)               │   │
│  │  - Receives JobAssignment(room="room-42")              │   │
│  │  - Creates RoomSession for assigned room               │   │
│  └───────────────────────────┬───────────────────────────┘   │
│                              │                                │
│  ┌───────────────────────────┴───────────────────────────┐   │
│  │          Per-Job RoomSession (auto-created)            │   │
│  │                                                        │   │
│  │  ┌─────────┐  ┌─────────┐  ┌───────────────┐         │   │
│  │  │ LiveKit │→│Deepgram │→│ AgentBridge   │         │   │
│  │  │ Audio In│  │ STT     │  │ (direct call) │         │   │
│  │  └─────────┘  └─────────┘  └───────┬───────┘         │   │
│  │                                     │                  │   │
│  │  ┌──────────┐  ┌──────────┐  ┌─────┴───────┐         │   │
│  │  │ LiveKit  │←│ElevenLabs│←│ ChatStream  │         │   │
│  │  │ Audio Out│  │ TTS      │  │ callback    │         │   │
│  │  └──────────┘  └──────────┘  └─────────────┘         │   │
│  └───────────────────────────────────────────────────────┘   │
│                                                               │
│  Imported from PicoClaw (Go library):                         │
│  - pkg/providers/  (LLM providers, fallback chains)           │
│  - pkg/session/    (conversation history, JSONL store)        │
│  - pkg/tools/      (tool registry)                            │
│  - pkg/config/     (config loading, security)                 │
│                                                               │
│  NOT used: MessageBus, Channel Manager, channels/*, AgentLoop │
└─────────────────────────────────────────────────────────────┘
```

### How Named Agent Dispatch Works

This is the **standard LiveKit agent protocol** — identical to Python/Node frameworks:

```
1. Worker starts:
   ./picoclaw-livekit --agent-name "support-bot"

   Worker opens WebSocket to LiveKit server
   Sends RegisterWorkerRequest { agent_name: "support-bot" }
   Server acknowledges registration

2. User/backend requests agent:
   Backend calls: CreateDispatch(agent_name="support-bot", room="room-42")
   OR: user token includes agent dispatch in RoomConfiguration

3. Server dispatches to worker:
   Server → AvailabilityRequest { job: { room: "room-42" } }
   Worker → AvailabilityResponse { accept: true }
   Server → JobAssignment { job: { id, room: "room-42", participant } }

4. Worker handles the job:
   Creates RoomSession for "room-42"
   Joins room as participant
   Starts Deepgram + TTS pipeline
   Handles voice conversation

5. Job ends (user leaves):
   Worker receives job termination or detects participant disconnect
   Cleans up RoomSession, Deepgram stream, TTS
   Worker stays registered, ready for next job

Concurrent jobs:
   Room-42, Room-99, Room-500 all handled simultaneously
   Each room is isolated goroutines (~3 goroutines + ~2-5MB per room)
   One worker process handles 1000+ rooms
```

### Key Architectural Decisions

- **Standard LiveKit protocol**: Uses the same WebSocket-based worker registration as Python/Node frameworks. From LiveKit server's perspective, this is a normal agent worker.
- **No room config needed**: Rooms are assigned dynamically by LiveKit server. No agent list in config.
- **No MessageBus**: The audio pipeline calls the agent directly via `AgentBridge`.
- **No Channel Manager / Channel interface**: Not a PicoClaw channel plugin.
- **Shared config**: Reuses `~/.picoclaw/config.json` and `.security.yml` for LLM provider settings.
- **Shared workspace**: Uses `~/.picoclaw/workspace/` for memory and session history.

## Component Design

### 1. CLI Binary (`cmd/picoclaw-livekit/`)

**File:** `cmd/picoclaw-livekit/main.go`

```bash
# Build
make build-livekit

# Run — register as named agent "support-bot"
./build/picoclaw-livekit --agent-name "support-bot"

# With custom config
./build/picoclaw-livekit --agent-name "support-bot" --config ~/.picoclaw/config.json
```

Entrypoint that:
- Parses `--agent-name` flag (required)
- Loads PicoClaw config from `~/.picoclaw/config.json`
- Applies security config from `.security.yml`
- Creates LLM provider from config (reuses `pkg/providers/` factory)
- Creates session store (reuses `pkg/session/`)
- Creates Worker with agent name
- Starts the worker (registers with LiveKit, blocks until signal)
- On SIGTERM: graceful shutdown (finish in-flight jobs, then exit)

### 2. Worker (`pkg/livekit/worker.go`)

Implements the LiveKit agent worker protocol via WebSocket.

```go
type Worker struct {
    agentName  string
    serverURL  string
    apiKey     string
    apiSecret  string
    conn       *websocket.Conn
    jobs       map[string]*RoomSession  // keyed by job ID
    mu         sync.RWMutex

    // Shared resources for creating per-job AgentBridges
    config     *config.Config
    provider   providers.LLMProvider
    sessions   session.SessionStore
    deepgram   *deepgram.DeepgramTranscriber
    ttsCfg     elevenlabs_tts.TTSConfig
}

func NewWorker(agentName string, cfg WorkerConfig) (*Worker, error)
func (w *Worker) Run(ctx context.Context) error   // register + listen loop
func (w *Worker) Shutdown() error                  // graceful shutdown
```

**Worker lifecycle:**
1. `Run()` opens WebSocket to `wss://<server>/agent`
2. Sends `RegisterWorkerRequest` protobuf with `agent_name`
3. Enters read loop:
   - `AvailabilityRequest` → respond with accept/reject
   - `JobAssignment` → create RoomSession, join room, start pipeline
   - `JobTermination` → clean up RoomSession
   - `WorkerPong` → heartbeat response
4. Sends `WorkerPing` periodically (keep-alive)
5. On disconnect: reconnect with exponential backoff

**Protocol messages** use protobuf types from `github.com/livekit/protocol/livekit`:
- `livekit.WorkerMessage` (worker → server)
- `livekit.ServerMessage` (server → worker)

### 3. AgentBridge (`pkg/livekit/agent_bridge.go`)

Simplified agent execution path. One instance per participant (per job).

```go
type AgentBridge struct {
    config   *config.Config
    provider providers.LLMProvider
    sessions session.SessionStore
    toolDefs []providers.ToolDefinition
    tools    *tools.ToolRegistry
}

func NewAgentBridge(cfg *config.Config, provider providers.LLMProvider, sessions session.SessionStore) *AgentBridge

func (ab *AgentBridge) ChatStream(ctx context.Context, sessionKey string, text string, cb func(chunk string)) error
```

`ChatStream` implements its own turn loop:
1. Load session history for the given session key
2. Build messages (system prompt + history + user message)
3. Call `StreamingProvider.ChatStream()` on the LLM provider
4. As text chunks arrive: fire `cb(chunk)` immediately (feeds TTS)
5. If tool calls detected in the response: execute tools, add results, loop back to step 3
6. Repeat until no more tool calls or max iterations reached
7. Save complete conversation to session store

**Tool call detection during streaming** is handled by buffering the streamed response while simultaneously forwarding text chunks. When the stream completes, the buffer is checked for tool calls. If found, text output pauses, tools execute, and the next LLM call streams its response.

Session keys: `livekit:<room_name>:<participant_identity>`

### 4. RoomSession (`pkg/livekit/room_session.go`)

Created per job assignment. Manages one agent in one room.

```go
type RoomSession struct {
    worker       *Worker
    jobID        string
    room         *lksdk.Room
    bridge       *AgentBridge              // per-job, isolated
    participant  *ParticipantState         // one user per room
    localTrack   *lksdk.LocalSampleTrack   // TTS audio output
    mu           sync.Mutex
    ctx          context.Context
    cancel       context.CancelFunc
}

type ParticipantState struct {
    identity       string
    sessionKey     string
    deepgramStream deepgram.TranscriptionStream
    ttsCancel      context.CancelFunc
    speaking       atomic.Bool
    mu             sync.Mutex
}
```

**Lifecycle:**
1. Created by Worker on `JobAssignment`
2. Generates participant token, joins room via `lksdk.ConnectToRoom()`
3. Creates local audio track for TTS output
4. On `OnTrackSubscribed`: starts audio pipeline for the participant
5. On participant disconnect or `JobTermination`: cleans up everything

Since each room has exactly one user, `RoomSession` tracks a single `ParticipantState` (not a map).

### 5. Audio Pipeline (`pkg/livekit/audio_pipeline.go`)

Same design as Approach A. Per-participant, 3 goroutines.

#### Inbound (user → agent)
```
LiveKit audio track (Opus) → PCM16 → Deepgram stream → speech end →
    AgentBridge.ChatStream(sessionKey, text, callback)
```

#### Outbound (agent → user)
```
ChatStream callback → sentence splitter → ElevenLabs TTS → PCM16 →
    LiveKit local audio track
```

#### Interruption
```
Deepgram speech start → cancel TTS context → flush audio track
Deepgram speech end → publish utterance to AgentBridge
```

### 6. Deepgram Streaming STT (`pkg/voice/deepgram/`)

**Identical to Approach A.** Shared package.

### 7. ElevenLabs TTS (`pkg/voice/elevenlabs_tts/`)

**Identical to Approach A.** Shared package.

## Config

### `config.json` (new top-level section)

```json
{
  "livekit_service": {
    "server_url": "wss://your-livekit-server.com",
    "tts": {
      "voice_id": "21m00Tcm4TlvDq8ikWAM",
      "model_id": "eleven_turbo_v2_5",
      "output_format": "pcm_24000"
    }
  }
}
```

No `agents` list — rooms are assigned dynamically by LiveKit server.
Agent name is passed via CLI flag: `--agent-name "support-bot"`.

### `.security.yml`

```yaml
livekit_service:
  api_key: "your-livekit-api-key"
  api_secret: "your-livekit-api-secret"
  deepgram_api_key: "your-deepgram-key"
```

ElevenLabs API key reuses existing `voice.elevenlabs_api_key`.

## Dependencies

### New Go Packages

| Package | Purpose |
|---|---|
| `github.com/livekit/server-sdk-go/v2` | Join rooms, publish/subscribe audio tracks, PCM16 APIs |
| `github.com/livekit/protocol` | Agent dispatch protobuf types, token generation |

`github.com/gorilla/websocket` already in `go.mod`.

### New Files

```
cmd/picoclaw-livekit/
    main.go                # CLI entrypoint with --agent-name flag

pkg/livekit/
    worker.go              # LiveKit agent worker (WebSocket protocol)
    worker_test.go         # Worker registration + job lifecycle tests
    room_session.go        # Per-job room lifecycle
    room_session_test.go   # Room session tests
    audio_pipeline.go      # Per-participant STT→Agent→TTS
    audio_pipeline_test.go # Pipeline + interruption tests
    agent_bridge.go        # Simplified agent execution path
    agent_bridge_test.go   # Bridge unit tests

pkg/voice/deepgram/        # Shared with Approach A
    types.go
    streaming.go
    streaming_test.go

pkg/voice/elevenlabs_tts/   # Shared with Approach A
    types.go
    tts.go
    tts_test.go
```

### Modified Files

- `pkg/config/config.go` — add `LiveKitServiceConfig` struct (top-level, not under `ChannelsConfig`)
- `pkg/config/security.go` — add `LiveKitServiceSecurity` struct
- `pkg/config/config.go` (in `applySecurityConfig`) — wire secrets
- `Makefile` — add `build-livekit` target

No changes to `pkg/gateway/`, `pkg/channels/`, `pkg/agent/`, or any existing binary.

## Scalability

### Per-Room Resources
- ~3 goroutines
- 1 Deepgram WebSocket
- 1 AgentBridge instance
- ~2-5 MB memory
- CPU only during active speech/TTS

### Scaling Model
- **1 worker process** handles 1000+ concurrent rooms
- **Multiple workers**: run N instances, LiveKit load-balances jobs across them
- **Horizontal scaling**: just start more worker processes, no config changes
- **Zero coordination**: workers are stateless (sessions persisted to disk), any worker can handle any room

### Failure Isolation
- One room crash (panic recovery) doesn't affect other rooms
- Worker reconnects to LiveKit on WebSocket disconnect
- Deepgram reconnects with backoff per-participant

## Comparison with Approach A

| Aspect | Approach A (Channel Plugin) | Approach C (Standalone Worker) |
|---|---|---|
| Binary | Single `picoclaw` binary | Separate `picoclaw-livekit` binary |
| Agent dispatch | Manual room config | **LiveKit native dispatch** (same as Python/Node) |
| Scalability | Single process, all channels | **Horizontal** (N workers, LiveKit load-balances) |
| Agent integration | Via MessageBus (pub/sub) | Direct provider/session import |
| Agent features | Full (all hooks, steering, compression) | Subset (sufficient for voice) |
| Room management | Configured in config.json | **Dynamic** (LiveKit assigns rooms) |
| Other channels alongside | Yes (Telegram, Discord etc.) | No (voice only) |
| Deployment | One process | Two processes (gateway + livekit worker) |
| Complexity | Fits existing patterns | New worker protocol + AgentBridge |
| Coupling to PicoClaw | Channel interface (stable) | Direct pkg imports |
| Streaming | Via bus StreamDelegate | Native ChatStream (lower latency) |
| Failure blast radius | Crash affects all channels | **Isolated** (only voice affected) |

## Testing Strategy

1. **Unit tests** for Worker (mock WebSocket, verify registration + job lifecycle)
2. **Unit tests** for AgentBridge (mock provider, verify ChatStream + tool calls)
3. **Unit tests** for Deepgram streaming (mock WebSocket) — shared with Approach A
4. **Unit tests** for ElevenLabs TTS (mock HTTP) — shared with Approach A
5. **Unit tests** for audio pipeline (mock STT/TTS/bridge, verify interruption)
6. **Integration spike** with real LiveKit server (validate worker registration)
7. **Manual testing** with LiveKit Playground web client
