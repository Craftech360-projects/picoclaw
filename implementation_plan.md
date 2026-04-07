# Goal: Support Multiple STT Providers for LiveKit Low-Latency Voice Agent

Currently, the `picoclaw-livekit` voice agent hardcodes its Speech-To-Text (STT) layer entirely to Deepgram. LiveKit's ecosystem supports various STT providers (AssemblyAI, Deepgram, ElevenLabs, OpenAI, Groq, Cartesia, etc.) for real-time streaming transciption.

This implementation plan outlines the architecture required to abstract out the current STT layer in `pkg/voice` and build a scalable STT Provider Factory pattern (mirroring the existing TTS implementation) so that new providers can be plugged in trivially.

## User Review Required

> [!WARNING]
> Before modifying the core `livekit` worker loop, I need you to review this architecture. By doing this, we will abstract away `deepgramStream` into a generic `TranscriptionStream` interface. This prepares the code to support multiple backends, but I need your approval to modify `pkg/voice/deepgram` and `room_session.go`.

## Proposed Changes

### 1. Configuration Layer

#### [MODIFY] `pkg/config/livekit.go` (or wherever LiveKit config is)
- Add an `STT` configuration struct to `LiveKitServiceConfig`.
- Map new fields: `Provider` (string), `ModelID` (string), `Language` (string).

---

### 2. Provider Abstraction / The STT Package

#### [NEW] `pkg/voice/stt/stt.go`
Define the generic interfaces that all streaming STT providers must adhere to (based on the current deepgram types):
```go
package stt

type StreamOpts struct {
	SampleRate     int
	Encoding       string
	Channels       int
	Language       string
    Model          string
}

type TranscriptEvent struct {
	Text        string
	IsFinal     bool
	SpeechStart bool
	SpeechEnd   bool
}

type TranscriptionStream interface {
	Results() <-chan TranscriptEvent
	SendAudio(pcm []byte) error
	Finalize() error
	Close() error
}

type Provider interface {
	OpenStream(opts StreamOpts) (TranscriptionStream, error)
}
```

#### [NEW] `pkg/voice/stt/factory.go`
Implement a Factory registry so providers can be initialized cleanly without tightly coupling `main.go` to every API client SDK:
```go
type ProviderBuilder func(cfg *config.Config, lkCfg config.LiveKitServiceConfig) Provider

type Factory struct {
	builders map[string]ProviderBuilder
}
// Implements NewFactory, Register, and Create()
```

---

### 3. Migrating Deepgram

#### [MODIFY] `pkg/voice/deepgram/streaming.go`
#### [MODIFY] `pkg/voice/deepgram/types.go`
- Refactor the code so that `deepgramStream` implements `stt.TranscriptionStream`.
- Remove the local interfaces and import `pkg/voice/stt`.

---

### 4. Updating the LiveKit Agent

#### [MODIFY] `pkg/livekit/room_session.go`
- **Replace**: `cfg.Deepgram *deepgram.DeepgramTranscriber` with `cfg.STT stt.Provider`.
- When dealing with incoming tracks inside `handleTrackSubscribed`, use `rs.stt.OpenStream()` generically. No Deepgram-specific logic should exist here.

#### [MODIFY] `cmd/picoclaw-livekit/main.go`
- Call the `stt.NewFactory()` on boot.
- Register `deepgram` and initial placeholders/adapters for kid-friendly providers:
  - `groq` (to use Whisper for high resilience to kid pitches)
  - `speechmatics` (for high inclusive accuracy)
  - `soapbox` (as an infrastructure placeholder for SoapBox Labs)
- Inject the chosen `stt.Provider` into the `RoomSessionConfig`.

---

## Open Questions

> [!IMPORTANT]
> 1. We will prioritize building the **Groq (Whisper)** and **Speechmatics** adapters first because they have excellent kid-speech recognition and standard APIs. Do you have API keys available for those?
> 2. Right now Deepgram relies heavily on `EndpointingMS` and specific `SpeechFinal` websocket events. Does the new STT provider need to support specific endpointing events natively, or are you comfortable with the existing TEN VAD pipeline taking over silence detection?

## Verification Plan

### Automated / Build
- Run `go build ./cmd/picoclaw-livekit` to ensure no interface breakages.
- Pass existing Deepgram tests.

### Manual Verification
- Join the LiveKit room with the refactored code using `deepgram` to ensure the abstraction didn't break real-time VAD or transcription mapping.
- Change the `livekit_service.stt.provider` param and test the pluggable nature.
