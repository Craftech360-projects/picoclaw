# LiveKit Voice Agent Channel - Design Spec

## Overview

Add a LiveKit voice channel to PicoClaw, enabling real-time voice conversations with the AI agent. Users speak into a LiveKit room, the agent transcribes via Deepgram streaming STT, processes through PicoClaw's existing agent loop (LLM + tools + memory), and responds with ElevenLabs TTS audio streamed back into the room.

## Requirements

- Voice-only LiveKit agent (no video)
- Multi-room, multi-user with separate sessions per participant
- On-demand named agents configured in `config.json`
- Deepgram streaming STT with built-in endpointing (no separate VAD)
- ElevenLabs streaming TTS for voice output
- Immediate interruption: cancel TTS when user starts speaking
- Reuse PicoClaw's full agent loop (LLM, tools, memory, streaming)
- No changes to core agent loop or existing channels

## Architecture

```
+-----------------------------------------------------+
|                   PicoClaw Process                    |
|                                                       |
|  +------------------------------------------------+  |
|  |           Channel Manager                       |  |
|  |  +----------+ +----------+ +--------------+    |  |
|  |  | Telegram | | Discord  | | LiveKit (new)|    |  |
|  |  +----------+ +----------+ +------+-------+    |  |
|  +----------------------------------------|-------+  |
|                                            |          |
|  +-------------- MessageBus --------------+|------+   |
|  |                                         |      |   |
|  |  +-------------------------------------+|      |   |
|  |  |         Agent Loop                   ||      |   |
|  |  |  (LLM + Tools + Memory)             ||      |   |
|  |  +--------------------------------------+|      |   |
|  +------------------------------------------+      |   |
|                                                       |
|  +------------------------------------------------+  |
|  |        LiveKit Channel Internals                |  |
|  |                                                 |  |
|  |  Per Room Instance (RoomSession):               |  |
|  |  +---------+    +---------+    +----------+    |  |
|  |  | LiveKit |---+| Deepgram|---+| Message  |    |  |
|  |  | Audio In|    | STT     |    |   Bus    |    |  |
|  |  +---------+    +---------+    +----------+    |  |
|  |                                                 |  |
|  |  +----------+   +----------+   +---------+     |  |
|  |  | LiveKit  |+--+ElevenLabs|+--| Agent   |     |  |
|  |  | Audio Out|   |  TTS     |   |Response |     |  |
|  |  +----------+   +----------+   +---------+     |  |
|  +------------------------------------------------+  |
+-------------------------------------------------------+
```

The LiveKit channel registers as a standard PicoClaw channel plugin via factory pattern. It embeds `BaseChannel` and communicates with the agent loop through the existing `MessageBus`.

## Component Design

### 1. LiveKit Channel (`pkg/channels/livekit/`)

#### Files

- `init.go` -- factory registration via `channels.RegisterFactory("livekit", ...)`
- `channel.go` -- main channel implementation, embeds `BaseChannel`
- `room_session.go` -- per-room participant lifecycle
- `audio_pipeline.go` -- coordinates STT, Agent, TTS flow per participant
- `config.go` -- LiveKit-specific config types

#### Config (`config.json`)

```json
"channels": {
  "livekit": {
    "enabled": true,
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
        "identity": "picoclaw-agent",
        "auto_join": true
      }
    ]
  }
}
```

`allow_from` restricts which LiveKit participant identities can interact with the agent. Empty array means all participants are allowed. Access control is also enforced at the LiveKit token level (only users with valid tokens can join rooms).

#### Config (`.security.yml`)

```yaml
channels:
  livekit:
    api_key: "your-livekit-api-key"
    api_secret: "your-livekit-api-secret"
    deepgram_api_key: "your-deepgram-key"
```

ElevenLabs API key reuses existing `voice.elevenlabs_api_key`.

#### Channel Lifecycle

- `Start()` -- for each configured agent with `auto_join: true`, create a RoomSession and join the room
- `Stop()` -- gracefully leave all rooms, close all Deepgram/TTS streams
- `Send()` -- receives agent text response from bus, routes to the correct RoomSession's TTS pipeline. Routing uses `OutboundMessage.ChatID` which is set to `<room>:<participant_identity>` (matching the `chatID` used when publishing inbound messages via `HandleMessage`). `Send()` parses this to find the target RoomSession and ParticipantState.

### 2. RoomSession (`room_session.go`)

Each RoomSession manages one agent participant in one LiveKit room.

#### State Machine

```
Created -> Connecting -> Connected -> Active (listening/speaking)
                                        |
                                User speaks -> STT -> Agent
                                Agent responds -> TTS -> Audio out
                                User interrupts -> Cancel TTS, listen
                                        |
                                    Disconnected
```

#### Responsibilities

- Join room using `livekit/server-sdk-go` with a generated participant token
- Subscribe to remote audio tracks (user's microphone)
- Decode incoming Opus audio to PCM16
- Forward PCM16 to per-participant Deepgram streaming connection
- Track per-participant sessions via `map[participantIdentity]*ParticipantState`
- Create new `ParticipantState` when a participant joins
- Clean up state when a participant leaves

#### ParticipantState

```go
type ParticipantState struct {
    identity       string                        // LiveKit participant identity
    sessionKey     string                        // "livekit:<room>:<identity>"
    deepgramStream deepgram.TranscriptionStream  // active WebSocket to Deepgram (interface)
    ttsCancel      context.CancelFunc            // cancel current TTS playback
    speaking       atomic.Bool                   // is user currently speaking
}
```

### 3. Deepgram Streaming STT (`pkg/voice/deepgram/`)

New package for real-time streaming speech-to-text. Separate from existing file-based transcribers.

#### Interface

```go
// StreamingTranscriber opens persistent streaming connections to Deepgram.
type StreamingTranscriber interface {
    OpenStream(ctx context.Context, opts StreamOpts) (TranscriptionStream, error)
}

// TranscriptionStream is the interface for an active Deepgram streaming session.
// Concrete implementation: deepgramStream (unexported struct in streaming.go).
type TranscriptionStream interface {
    // Feed PCM16 audio chunks from LiveKit
    SendAudio(pcm []byte) error

    // Receive transcription events (final results, speech end)
    Results() <-chan TranscriptEvent

    // Close the WebSocket connection
    Close() error
}

type TranscriptEvent struct {
    Text      string  // transcribed text
    IsFinal   bool    // final vs interim result
    SpeechEnd bool    // endpointing detected (user stopped talking)
}
```

#### Implementation

- WebSocket connection to `wss://api.deepgram.com/v1/listen`
- Parameters: `encoding=linear16`, `sample_rate=48000`, `endpointing=300` (300ms silence = speech end)
- Sends PCM16 audio chunks as binary WebSocket frames
- Receives JSON transcription results
- `IsFinal=true` events accumulate into a complete utterance
- `SpeechEnd=true` triggers publishing the full utterance to the agent
- Reconnects with backoff on connection drop

### 4. ElevenLabs TTS (`pkg/voice/elevenlabs_tts/`)

New package for streaming text-to-speech output.

#### Interface

```go
type TTSProvider interface {
    Synthesize(ctx context.Context, text string) (*AudioStream, error)
}

type AudioStream struct {
    // Read PCM16 chunks as they arrive from the TTS API
    Read() ([]byte, error)

    // Cancel and close the stream
    Close() error
}
```

#### Implementation

- Uses ElevenLabs streaming TTS API: `POST /v1/text-to-speech/{voice_id}/stream`
- Default output format `pcm_24000` returns raw PCM16 directly -- no MP3 decoding needed
- Streams PCM16 chunks directly to LiveKit audio track as they arrive
- Context-cancellable: when user interrupts, HTTP request is cancelled immediately
- If a different output format like MP3 is configured, an MP3 decoder step is added

#### Sentence Chunking for Low Latency

1. Agent response text arrives (possibly streaming from LLM)
2. Accumulate text until sentence boundary (`.` `!` `?`)
3. Send sentence to ElevenLabs TTS
4. Stream audio chunks to LiveKit track as they arrive
5. Meanwhile, accumulate next sentence
6. If interrupted, cancel current HTTP request, stop playback

### 5. Audio Pipeline & Interruption (`audio_pipeline.go`)

Coordinates the full voice loop per participant within a RoomSession.

#### Inbound Flow (user to agent)

```
LiveKit audio track (Opus)
    -> Decode to PCM16
    -> Feed to Deepgram stream
    -> On SpeechEnd -> build full utterance
    -> Publish InboundMessage to MessageBus
        sessionKey: "livekit:<room>:<identity>"
        content: transcribed text
```

#### Outbound Flow (agent to user)

```
Agent response text arrives via Send()
    -> Sentence splitter buffers text
    -> Per sentence -> ElevenLabs TTS stream
    -> PCM16 chunks -> LiveKit local audio track (Opus encode)
    -> Publish to room
```

#### Interruption Flow

```
Deepgram fires speech start event
    -> Set participant.speaking = true
    -> Cancel active TTS context
    -> Flush remaining audio from local track
    -> Deepgram continues transcribing new utterance

Deepgram fires speech end event
    -> Set participant.speaking = false
    -> Publish transcribed text to agent
```

#### Concurrency Model

Each participant has 3 goroutines:

1. **Audio reader** -- reads from LiveKit track, feeds Deepgram
2. **Transcript reader** -- reads Deepgram events, publishes to bus
3. **TTS writer** -- receives agent response, streams TTS to track

All coordinated via contexts. Interruption cancels TTS writer's context. No shared mutable state beyond `speaking` bool (atomic).

#### Edge Cases

- User speaks before agent finishes: immediate TTS cancel
- Multiple rapid utterances: each queued as separate messages
- Deepgram connection drops: reconnect with backoff
- ElevenLabs request fails: log error, skip audio (text still delivered if hybrid)
- Participant leaves mid-response: cancel all goroutines via context

## Dependencies

### New Go Packages

| Package | Purpose |
|---|---|
| `github.com/livekit/server-sdk-go/v2` | Join rooms, publish/subscribe audio tracks, PCM16 APIs |
| `github.com/livekit/protocol` | LiveKit types, token generation |
| `github.com/hajimehoshi/go-mp3` | Optional: only needed if MP3 TTS output format is configured |

`github.com/gorilla/websocket` is already in `go.mod` (used for Deepgram WebSocket).

#### LiveKit SDK Usage Details

The `livekit/server-sdk-go/v2` participant API is used as follows:

- **Joining a room:** `lksdk.ConnectToRoom(serverURL, token, roomCallback)` returns a `*lksdk.Room`
- **Subscribing to audio:** `room.Callback.OnTrackSubscribed` fires when a participant publishes audio. The received `*webrtc.TrackRemote` is read for Opus packets, decoded to PCM16 via the SDK's `NewPCMRemoteTrack` helper.
- **Publishing audio:** `lksdk.NewPCMLocalTrack(sampleRate, channels)` creates a local audio track that accepts PCM16 samples. Published via `room.LocalParticipant.PublishTrack(track, opts)`.
- **Opus encode/decode:** Handled internally by the SDK's PCM track helpers.
- **Token generation:** `auth.NewAccessToken(apiKey, apiSecret)` with `VideoGrant{RoomJoin: true, Room: roomName}`.

A validation spike should confirm PCM16 round-trip (subscribe Opus -> decode -> re-encode -> publish) works correctly before full implementation.

### New Files

```
pkg/channels/livekit/
    init.go              # Factory registration
    channel.go           # Channel impl, embeds BaseChannel
    room_session.go      # Per-room lifecycle + participant tracking
    audio_pipeline.go    # Coordinates STT->Agent->TTS per participant
    config.go            # LiveKit channel config types

pkg/voice/deepgram/
    streaming.go         # Deepgram WebSocket streaming STT
    types.go             # Event types

pkg/voice/elevenlabs_tts/
    tts.go               # ElevenLabs streaming TTS
    types.go             # Config/response types
```

### Modified Files (minimal)

- `pkg/config/config.go` -- add `LiveKitConfig` struct to `ChannelsConfig`, with `env:"PICOCLAW_CHANNELS_LIVEKIT_..."` struct tags for env var overrides
- `pkg/config/security.go` -- add `LiveKitSecurity` struct (fields: `APIKey`, `APISecret`, `DeepgramAPIKey`) and add `LiveKit *LiveKitSecurity` field to `ChannelsSecurity`
- `pkg/config/config.go` (in `applySecurityConfig`) -- wire LiveKit secrets from `.security.yml` into config
- `pkg/gateway/gateway.go` -- add blank import `_ "github.com/sipeed/picoclaw/pkg/channels/livekit"` for factory registration (this is where all channel imports live, not `main.go`)

No changes to the agent loop, message bus, session store, or any existing channel.

### Startup Behavior

When `enabled: false`, the factory short-circuits and the channel is not created (consistent with other channels). If Deepgram is unreachable at startup, the channel logs an error and retries connection with backoff. The channel does not block gateway startup.

## Session Isolation

Each participant in each room gets a unique session key: `livekit:<room_name>:<participant_identity>`

This maps directly to PicoClaw's existing session system:
- Separate conversation history per participant
- Separate memory context
- Separate tool state
- No cross-contamination between rooms or participants

## Testing Strategy

1. **Unit tests** for Deepgram streaming (mock WebSocket, verify event parsing)
2. **Unit tests** for ElevenLabs TTS (mock HTTP, verify MP3 decode + streaming)
3. **Unit tests** for audio pipeline (mock STT/TTS, verify interruption flow)
4. **Integration test** with real LiveKit server (local `livekit-server` binary)
5. **Manual testing** with LiveKit Playground web client for end-to-end voice
