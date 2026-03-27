# LiveKit Voice Agent Standalone Service - Design Spec (Approach C)

## Overview

Build a standalone `picoclaw-livekit` binary that joins LiveKit rooms as a voice agent. It imports PicoClaw's agent packages directly (`pkg/agent/`, `pkg/providers/`, `pkg/session/`) for full agent quality, transcribes user speech via Deepgram streaming STT, and responds with ElevenLabs TTS. Runs as its own process, independent of PicoClaw's gateway and channel system.

## Requirements

- Voice-only LiveKit agent (no video)
- Multi-room, multi-user with separate sessions per participant
- On-demand named agents configured in `config.json`
- Deepgram streaming STT with built-in endpointing (no separate VAD)
- ElevenLabs streaming TTS for voice output
- Immediate interruption: cancel TTS when user starts speaking
- Direct import of PicoClaw's provider and session packages (not the full agent loop)
- Separate binary: does not modify PicoClaw gateway or channel system

## Important Limitations

AgentBridge is **NOT a wrapper around PicoClaw's `AgentLoop.runTurn()`**. The existing turn loop (~900 lines) is deeply coupled to `MessageBus` and cannot be invoked without it. AgentBridge instead builds a **simplified agent execution path** that:

- Calls `StreamingProvider.ChatStream()` directly for token-by-token streaming
- Handles tool call iterations in its own loop
- Uses PicoClaw's session store, provider factory, and tool registry

**Features NOT available in Approach C** (present in Approach A):
- Steering messages (mid-turn user interruptions via text)
- Sub-agent spawning (`spawn` tool)
- Mid-turn context compression and summarization
- Tool approval hooks (BeforeTool/AfterTool interceptors)
- Event bus emissions (turn start/end, LLM retries)
- Graceful abort cascades from parent turns

For a voice agent, most of these are irrelevant (steering is voice-native via interruption, sub-agents are unlikely in voice). But the agent quality is a **subset**, not identical.

## Architecture

```
+-----------------------------------------------------------+
|              picoclaw-livekit binary                        |
|              (cmd/picoclaw-livekit/)                        |
|                                                             |
|  +---------------------------------------------------------+
|  |          LiveKit Service (pkg/livekit/)                  |
|  |                                                          |
|  |  Per Room (RoomSession):                                 |
|  |  +---------+    +---------+    +---------------+        |
|  |  | LiveKit |---+| Deepgram|---+| AgentBridge  |        |
|  |  | Audio In|    | STT     |    | (direct call)|        |
|  |  +---------+    +---------+    +-------+-------+        |
|  |                                        |                 |
|  |  +----------+   +----------+   +-------+-------+        |
|  |  | LiveKit  |+--+ElevenLabs|+--| ChatStream   |        |
|  |  | Audio Out|   | TTS      |   | callback     |        |
|  |  +----------+   +----------+   +---------------+        |
|  |                                                          |
|  |  Imported from PicoClaw (Go library):                    |
|  |  - pkg/agent/     (AgentLoop, AgentInstance)             |
|  |  - pkg/providers/  (LLM providers, fallback chains)      |
|  |  - pkg/session/    (conversation history, JSONL store)   |
|  |  - pkg/tools/      (tool registry)                       |
|  |  - pkg/memory/     (long-term memory)                    |
|  |  - pkg/config/     (config loading, security)            |
|  |                                                          |
|  |  NOT used: MessageBus, Channel Manager, channels/*       |
|  +---------------------------------------------------------+
+-------------------------------------------------------------+
```

### Key Architectural Decisions

- **No MessageBus**: The audio pipeline calls the agent loop directly via `AgentBridge`, bypassing the publish/subscribe message bus entirely.
- **No Channel Manager**: No retry orchestration, placeholder management, or rate limiting — these are text-channel concerns irrelevant to voice.
- **No Channel interface**: `pkg/livekit/` does not implement `channels.Channel` or embed `BaseChannel`. It lives outside `pkg/channels/`.
- **Shared config**: Reuses `~/.picoclaw/config.json` and `.security.yml` for LLM provider settings, API keys, and agent defaults.
- **Shared workspace**: Uses the same `~/.picoclaw/workspace/` for memory, skills, and session history.

## Component Design

### 1. CLI Binary (`cmd/picoclaw-livekit/`)

**File:** `cmd/picoclaw-livekit/main.go`

Entrypoint that:
- Loads PicoClaw config from `~/.picoclaw/config.json`
- Applies security config from `.security.yml`
- Creates LLM provider from config (reuses `pkg/providers/` factory)
- Creates session store (reuses `pkg/session/`)
- Creates AgentBridge wrapping the agent instance
- Creates LiveKitService with configured rooms
- Starts the service and blocks until signal

```bash
# Build
make build-livekit

# Run
./build/picoclaw-livekit
./build/picoclaw-livekit --config ~/.picoclaw/config.json
```

### 2. AgentBridge (`pkg/livekit/agent_bridge.go`)

Builds a simplified agent execution path using PicoClaw's provider, session, and tool packages directly — NOT wrapping `AgentLoop.runTurn()`.

```go
type AgentBridge struct {
    config      *config.Config
    provider    providers.LLMProvider
    sessions    session.SessionStore
    toolDefs    []providers.ToolDefinition
    tools       *tools.ToolRegistry
}

// NewAgentBridge creates one bridge per participant (to isolate tool state).
func NewAgentBridge(cfg *config.Config, provider providers.LLMProvider, sessions session.SessionStore) *AgentBridge

// ChatStream sends a message and streams response tokens via callback.
// Critical for low-latency TTS: we start speaking as soon as the first sentence arrives.
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

**One AgentBridge per participant** to avoid shared mutable tool state across concurrent sessions.

Session keys follow the format: `livekit:<room_name>:<participant_identity>`

### 3. LiveKit Service (`pkg/livekit/service.go`)

Top-level service that manages room sessions.

```go
type Service struct {
    config      LiveKitServiceConfig
    fullConfig  *config.Config            // for creating per-participant AgentBridges
    provider    providers.LLMProvider     // shared (thread-safe)
    sessions    session.SessionStore      // shared (thread-safe)
    rooms       map[string]*RoomSession
    deepgram    *deepgram.DeepgramTranscriber
    tts         *elevenlabs_tts.ElevenLabsTTS
}

func NewService(cfg LiveKitServiceConfig, fullCfg *config.Config, provider providers.LLMProvider, sessions session.SessionStore) (*Service, error)
func (s *Service) Start(ctx context.Context) error  // joins all configured rooms
func (s *Service) Stop() error                       // leaves all rooms
func (s *Service) NewBridgeForParticipant() *AgentBridge // creates isolated bridge
```

Provider and session store are shared (they are thread-safe). AgentBridge is created per-participant to isolate tool registry state.

### 4. RoomSession (`pkg/livekit/room_session.go`)

Identical behavior to Approach A:
- Joins LiveKit room using `livekit/server-sdk-go/v2` with generated participant token
- Subscribes to remote audio tracks
- Tracks participants via `map[identity]*ParticipantState`
- Creates/cleans up participant state on join/leave

```go
type ParticipantState struct {
    identity       string
    sessionKey     string                        // "livekit:<room>:<identity>"
    deepgramStream deepgram.TranscriptionStream  // active Deepgram WebSocket
    ttsCancel      context.CancelFunc            // cancel current TTS playback
    speaking       atomic.Bool                   // is user currently speaking
    mu             sync.Mutex                    // protects deepgramStream and ttsCancel
}
```

### 5. Audio Pipeline (`pkg/livekit/audio_pipeline.go`)

Coordinates STT, Agent, TTS per participant. Same 3-goroutine model as Approach A.

#### Inbound Flow (user to agent)

```
LiveKit audio track (Opus)
    -> Decode to PCM16
    -> Feed to Deepgram stream
    -> On SpeechEnd -> build full utterance
    -> Call AgentBridge.ChatStream(sessionKey, text, callback)
```

#### Outbound Flow (agent to user)

```
ChatStream callback fires with text chunks
    -> Sentence splitter buffers text
    -> Per sentence -> ElevenLabs TTS stream
    -> PCM16 chunks -> LiveKit local audio track (Opus encode)
    -> Publish to room
```

The key simplification vs Approach A: **no ChatID parsing or message routing**. The `ChatStream` callback is already scoped to the correct participant — it feeds directly into their TTS pipeline.

#### Interruption Flow

```
Deepgram fires speech start event (interim result with text)
    -> Set participant.speaking = true
    -> Cancel active TTS context (immediate barge-in)
    -> Flush remaining audio from local track

Deepgram fires speech end event
    -> Set participant.speaking = false
    -> Build full utterance from accumulated finals
    -> Call AgentBridge.ChatStream()
```

### 6. Deepgram Streaming STT (`pkg/voice/deepgram/`)

**Identical to Approach A.** Same package, same code. Shared between both approaches.

- WebSocket to `wss://api.deepgram.com/v1/listen`
- Parameters: `encoding=linear16`, `sample_rate=48000`, `endpointing=300`
- Thread-safe with write mutex
- Reconnects with backoff on connection drop

### 7. ElevenLabs TTS (`pkg/voice/elevenlabs_tts/`)

**Identical to Approach A.** Same package, same code. Shared between both approaches.

- Streaming API: `POST /v1/text-to-speech/{voice_id}/stream`
- Default `pcm_24000` output format (raw PCM16, no MP3 decode)
- Context-cancellable for immediate interruption

## Config

### `config.json` (new top-level section)

```json
{
  "livekit_service": {
    "server_url": "wss://your-livekit-server.com",
    "allow_from": [],
    "tts": {
      "provider": "elevenlabs",
      "voice_id": "21m00Tcm4TlvDq8ikWAM",
      "model_id": "eleven_turbo_v2_5",
      "output_format": "pcm_24000"
    },
    "agents": [
      {
        "name": "support-bot",
        "room": "support-room-1",
        "identity": "picoclaw-agent"
      }
    ]
  }
}
```

`allow_from` restricts which LiveKit participant identities can interact with the agent. Empty means all allowed.

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
| `github.com/livekit/protocol` | LiveKit types, token generation |

`github.com/gorilla/websocket` already in `go.mod`.

### LiveKit SDK Usage Details

- **Joining:** `lksdk.ConnectToRoom(serverURL, token, roomCallback)`
- **Subscribing audio:** `room.Callback.OnTrackSubscribed` with `*webrtc.TrackRemote`
- **Publishing audio:** `lksdk.NewPCMLocalTrack(sampleRate, channels)` + `room.LocalParticipant.PublishTrack()`
- **Token generation:** `auth.NewAccessToken(apiKey, apiSecret)` with `VideoGrant{RoomJoin: true, Room: roomName}`

A validation spike should confirm PCM16 round-trip before full implementation.

### New Files

```
cmd/picoclaw-livekit/
    main.go              # CLI entrypoint

pkg/livekit/
    service.go           # Top-level service, manages rooms
    room_session.go      # Per-room lifecycle + participant tracking
    audio_pipeline.go    # Per-participant STT->Agent->TTS
    agent_bridge.go      # Direct agent loop wrapper
    agent_bridge_test.go # Bridge unit tests

pkg/voice/deepgram/      # Shared with Approach A
    types.go
    streaming.go
    streaming_test.go

pkg/voice/elevenlabs_tts/ # Shared with Approach A
    types.go
    tts.go
    tts_test.go
```

### Modified Files

- `pkg/config/config.go` — add `LiveKitServiceConfig` struct (top-level field on `Config`, not under `ChannelsConfig`)
- `pkg/config/security.go` — add `LiveKitServiceSecurity` struct and field
- `pkg/config/config.go` (in `applySecurityConfig`) — wire LiveKit service secrets
- `Makefile` — add `build-livekit` target

No changes to `pkg/gateway/`, `pkg/channels/`, `pkg/agent/`, or any existing binary.

## Session Isolation

Same as Approach A. Each participant gets: `livekit:<room_name>:<participant_identity>`

- Separate conversation history per participant
- Separate memory context
- Separate tool state

## Comparison with Approach A

| Aspect | Approach A (Channel Plugin) | Approach C (Standalone Binary) |
|---|---|---|
| Binary | Single `picoclaw` binary | Separate `picoclaw-livekit` binary |
| Agent integration | Via MessageBus (pub/sub) | Direct provider/session import |
| Agent features | Full (all hooks, steering, compression) | Subset (no hooks, steering, compression) |
| Channel Manager features | Retries, rate limiting, placeholders | None (not needed for voice) |
| Other channels alongside | Yes (Telegram, Discord etc.) | No (voice only) |
| Code reuse | Deepgram + TTS shared | Deepgram + TTS shared |
| Deployment | One process | Two processes (if also running gateway) |
| Complexity | Fits existing patterns | New patterns (AgentBridge ~200 lines of new turn logic) |
| Coupling to PicoClaw | Channel interface contract | Direct pkg imports (breaks if internals change) |
| Latency | Bus overhead (minimal) | No bus (slightly lower) |
| Maintenance risk | Low (uses stable channel API) | Higher (parallel turn loop may diverge from main agent) |
| Streaming | Via bus StreamDelegate | Native ChatStream (simpler) |

## Testing Strategy

1. **Unit tests** for AgentBridge (mock agent instance, verify Chat/ChatStream)
2. **Unit tests** for Deepgram streaming (mock WebSocket) — shared with Approach A
3. **Unit tests** for ElevenLabs TTS (mock HTTP) — shared with Approach A
4. **Unit tests** for audio pipeline (mock STT/TTS/bridge, verify interruption)
5. **Integration spike** with real LiveKit server
6. **Manual testing** with LiveKit Playground web client
