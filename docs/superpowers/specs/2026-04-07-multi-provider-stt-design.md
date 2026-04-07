# Multi-Provider STT System Design

## Goal

Enable configurable speech-to-text provider selection for the LiveKit voice agent, with provider configuration stored in SQLite and selected at room join time. Support the comprehensive ecosystem of STT providers documented by LiveKit.

## Architecture

Replace the current hardcoded Deepgram-only STT with a factory-based multi-provider system following the existing TTS provider pattern.

## Tech Stack

- **Language**: Go 1.25+
- **Database**: SQLite (via `modernc.org/sqlite`)
- **Existing Patterns**: `pkg/voice/tts` factory, `pkg/voice/deepgram` streaming
- **LiveKit**: Server SDK v2, WebRTC audio tracks

## Current State

- STT is hardcoded to Deepgram in `cmd/picoclaw-livekit/main.go:76-79`
- `RoomSession` receives a concrete `*deepgram.DeepgramTranscriber`
- No abstraction for provider selection
- No database storage for provider configuration

## Comprehensive Provider Support

Based on LiveKit's STT documentation, here are all providers that should be supported:

### Tier 1: LiveKit Inference Providers (Managed)
These are directly integrated with LiveKit Inference:
- **AssemblyAI** - Universal-3 Pro Streaming (6 languages), Universal-Streaming (English only)
- **Cartesia** - Ink Whisper (100 languages)
- **Deepgram** - Flux, Nova-2 (English variants), Nova-3 (44 languages, multilingual mode)
- **ElevenLabs** - Scribe v2 Realtime (190 languages)

### Tier 2: Major API Providers
Popular cloud-based STT services:
- **OpenAI** - Whisper models (via API)
- **Azure AI Speech** - Microsoft's speech-to-text service
- **Google Cloud Speech-to-Text** - Google's STT API
- **AWS Transcribe** - Amazon's transcription service
- **Groq** - Fast Whisper inference (whisper-large-v3)

### Tier 3: Specialized STT Providers
Providers with specific strengths:
- **Gladia** - Real-time multilingual transcription
- **Soniox** - High-accuracy transcription with diarization
- **Speechmatics** - Enterprise transcription with diarization
- **Sarvam** - Regional language support (India, etc.)
- **Voxtral-mini-latest** - Lightweight models
- **Mistral AI** - Speech models
- **Gradium** - Speech-to-text API
- **Simplismart** - Specialized transcription
- **Spitch** - Speech services
- **Baseten** - Inference platform
- **Clova** - Naver's speech services
- **fal** - Fast inference
- **Nvidia** - GPU-accelerated inference
- **OVHCloud** - Cloud STT services

### Special Features by Provider

**Automatic Multilingual:**
- Deepgram Nova-3 (language set to "multi")
- ElevenLabs Scribe v2
- Cartesia Ink Whisper

**Diarization (Python Only):**
- AssemblyAI
- Deepgram
- Speechmatics
- Soniox

**Stream Adaptation Required:**
Non-streaming models (like OpenAI Whisper) need a StreamAdapter to buffer audio until VAD end-of-speech detection.

## Proposed Design

### 1. STT Provider Interface

**File**: `pkg/voice/stt/provider.go`

```go
package stt

import (
    "context"
)

// Provider defines a speech-to-text provider.
type Provider interface {
    // Name returns the provider identifier (e.g., "deepgram", "assemblyai")
    Name() string

    // Capabilities returns supported features
    Capabilities() ProviderCapabilities

    // OpenStream starts a streaming transcription session.
    OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error)
}

// ProviderCapabilities describes what the provider supports
type ProviderCapabilities struct {
    Languages        []string // Supported language codes
    Models           []string // Available model IDs
    SupportsStreaming    bool
    SupportsDiarization  bool
    SupportsMultilingual bool
    MaxAudioDuration     int // seconds, 0 = unlimited
}

// TranscriptionStream is a bidirectional audio stream with transcription results.
type TranscriptionStream interface {
    // SendAudio sends PCM audio data (16-bit little-endian).
    SendAudio(pcm []byte) error

    // Results returns a channel of transcription events.
    Results() <-chan TranscriptEvent

    // Finalize signals end of utterance to flush pending results.
    Finalize() error

    // Close closes the stream.
    Close() error
}

// TranscriptEvent represents a transcription result.
type TranscriptEvent struct {
    Text        string  // Transcribed text
    IsFinal     bool    // Whether this is a final result
    SpeechStart bool    // Speech start detected
    SpeechEnd   bool    // Speech end detected
    Confidence  float64 // Confidence score (0-1)
    Language    string  // Detected language
    Duration    float64 // Audio duration in seconds
    SpeakerID   string  // Speaker ID (if diarization enabled)
}

// StreamOptions configures a transcription stream.
type StreamOptions struct {
    SampleRate       int     // Audio sample rate (Hz)
    Channels         int     // Number of audio channels
    Language         string  // Target language code (e.g., "en", "hi", "auto")
    Model            string  // Model identifier (provider-specific)
    InterimResults   bool    // Enable interim (partial) results
    EndpointingMS    int     // Silence duration before speech end (ms)
    EnableDiarization bool   // Enable speaker diarization
    CustomConfig     map[string]any // Provider-specific configuration
}
```

### 2. Provider Factory

**File**: `pkg/voice/stt/factory.go`

```go
package stt

import (
    "database/sql"
    "fmt"
    "sync"

    _ "modernc.org/sqlite"
)

// Factory creates STT providers based on database configuration.
type Factory struct {
    dbPath    string
    db        *sql.DB
    providers map[string]Provider
    mu        sync.RWMutex
}

// NewFactory creates a new STT provider factory.
func NewFactory(dbPath string) (*Factory, error) {
    db, err := initDB(dbPath)
    if err != nil {
        return nil, fmt.Errorf("init STT DB: %w", err)
    }

    f := &Factory{
        dbPath:    dbPath,
        db:        db,
        providers: make(map[string]Provider),
    }

    // Register all built-in providers
    f.registerBuiltInProviders()

    return f, nil
}

// GetActiveProvider returns the currently active STT provider.
func (f *Factory) GetActiveProvider() (Provider, error) {
    f.mu.RLock()
    defer f.mu.RUnlock()

    var providerName, model string
    err := f.db.QueryRow(
        "SELECT provider_name, model FROM stt_providers WHERE is_active = 1 LIMIT 1",
    ).Scan(&providerName, &model)

    if err == sql.ErrNoRows {
        // Default to Deepgram if no provider is active
        providerName = "deepgram"
        model = "nova-2"
    } else if err != nil {
        return nil, fmt.Errorf("query active provider: %w", err)
    }

    provider, ok := f.providers[providerName]
    if !ok {
        return nil, fmt.Errorf("provider %q not registered", providerName)
    }

    return provider, nil
}

// GetProviderWithConfig returns a provider with specific configuration.
func (f *Factory) GetProviderWithConfig(name, apiKey, model string) (Provider, error) {
    baseProvider, ok := f.providers[name]
    if !ok {
        return nil, fmt.Errorf("provider %q not registered", name)
    }

    return baseProvider.WithConfig(apiKey, model)
}

// ListProviders returns all registered provider names.
func (f *Factory) ListProviders() []string {
    f.mu.RLock()
    defer f.mu.RUnlock()

    names := make([]string, 0, len(f.providers))
    for name := range f.providers {
        names = append(names, name)
    }
    return names
}

// UpdateProviderConfig updates or inserts provider configuration.
func (f *Factory) UpdateProviderConfig(name, apiKey, model string, isActive bool, priority int) error {
    _, err := f.db.Exec(`
        INSERT INTO stt_providers (provider_name, api_key, model, is_active, priority)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(provider_name) DO UPDATE SET
            api_key = excluded.api_key,
            model = excluded.model,
            is_active = excluded.is_active,
            priority = excluded.priority,
            updated_at = CURRENT_TIMESTAMP
    `, name, apiKey, model, isActive, priority)

    return err
}

// SetActiveProvider marks a provider as active (deactivates others).
func (f *Factory) SetActiveProvider(name string) error {
    _, err := f.db.Exec(`
        UPDATE stt_providers SET is_active = 0 WHERE provider_name != ?;
        UPDATE stt_providers SET is_active = 1 WHERE provider_name = ?;
    `, name, name)
    return err
}

// registerBuiltInProviders registers all compiled-in STT providers.
func (f *Factory) registerBuiltInProviders() {
    // Tier 1: LiveKit Inference Providers
    f.providers["deepgram"] = &deepgramProvider{}
    f.providers["assemblyai"] = &assemblyAIProvider{}
    f.providers["cartesia"] = &cartesiaProvider{}
    f.providers["elevenlabs"] = &elevenLabsProvider{}

    // Tier 2: Major API Providers
    f.providers["openai"] = &openAIProvider{}
    f.providers["azure"] = &azureProvider{}
    f.providers["google"] = &googleProvider{}
    f.providers["aws"] = &awsProvider{}
    f.providers["groq"] = &groqProvider{}

    // Tier 3: Specialized Providers
    f.providers["gladia"] = &gladiaProvider{}
    f.providers["soniox"] = &sonioxProvider{}
    f.providers["speechmatics"] = &speechmaticsProvider{}
    f.providers["sarvam"] = &sarvamProvider{}
    f.providers["mistral"] = &mistralProvider{}
}

func initDB(dbPath string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", dbPath)
    if err != nil {
        return nil, err
    }

    // Create schema
    _, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS stt_providers (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            provider_name TEXT NOT NULL UNIQUE,
            api_key TEXT NOT NULL,
            model TEXT NOT NULL DEFAULT '',
            language TEXT,
            sample_rate INTEGER DEFAULT 16000,
            is_active BOOLEAN NOT NULL DEFAULT 0,
            priority INTEGER DEFAULT 0,
            config_json TEXT,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
        );

        CREATE INDEX IF NOT EXISTS idx_stt_active ON stt_providers(is_active);

        -- Seed with common providers
        INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
        VALUES
            ('deepgram', '', 'nova-2', 0, 1),
            ('assemblyai', '', 'universal-3', 0, 2),
            ('cartesia', '', 'ink-whisper', 0, 3),
            ('elevenlabs', '', 'scribe-v2', 0, 4),
            ('openai', '', 'whisper-1', 0, 5),
            ('azure', '', 'speech-to-text', 0, 6),
            ('google', '', 'latest_long', 0, 7),
            ('aws', '', 'transcribe', 0, 8),
            ('groq', '', 'whisper-large-v3', 0, 9),
            ('gladia', '', 'latest', 0, 10),
            ('soniox', '', 'soniox-pro', 0, 11),
            ('speechmatics', '', 's2t', 0, 12),
            ('sarvam', '', 'sarvam-v1', 0, 13),
            ('mistral', '', 'mistral-speech', 0, 14);
    `)

    if err != nil {
        return nil, err
    }

    return db, nil
}
```

### 3. Example Provider Implementations

#### Deepgram Provider (Existing, Refactored)

**File**: `pkg/voice/stt/deepgram_provider.go`

```go
package stt

import (
    "context"
    "github.com/sipeed/picoclaw/pkg/voice/deepgram"
)

type deepgramProvider struct{}

func (p *deepgramProvider) Name() string { return "deepgram" }

func (p *deepgramProvider) Capabilities() ProviderCapabilities {
    return ProviderCapabilities{
        Languages:        []string{"en", "es", "fr", "de", "hi", "multi"},
        Models:           []string{"nova-3", "nova-2", "flux"},
        SupportsStreaming:    true,
        SupportsDiarization:  true,
        SupportsMultilingual: true,
    }
}

func (p *deepgramProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
    // Configure Deepgram-specific options
    apiKey := getConfigValue("DEEPGRAM_API_KEY", opts.CustomConfig)

    dg := deepgram.NewDeepgramTranscriber(apiKey)

    streamOpts := deepgram.StreamOpts{
        SampleRate:     opts.SampleRate,
        Language:       opts.Language,
        Model:          opts.Model,
        InterimResults: opts.InterimResults,
        EndpointingMS:  opts.EndpointingMS,
    }

    stream, err := dg.OpenStream(streamOpts)
    if err != nil {
        return nil, err
    }

    return &deepgramStreamAdapter{stream: stream}, nil
}

// deepgramStreamAdapter wraps the existing Deepgram stream
type deepgramStreamAdapter struct {
    stream deepgram.TranscriptionStream
}

func (a *deepgramStreamAdapter) SendAudio(pcm []byte) error {
    return a.stream.SendAudio(pcm)
}

func (a *deepgramStreamAdapter) Results() <-chan TranscriptEvent {
    // Convert deepgram events to standard events
    out := make(chan TranscriptEvent, 10)
    go func() {
        defer close(out)
        for evt := range a.stream.Results() {
            out <- TranscriptEvent{
                Text:      evt.Text,
                IsFinal:   evt.IsFinal,
                SpeechStart: evt.SpeechStart,
                SpeechEnd:   evt.SpeechEnd,
            }
        }
    }()
    return out
}

func (a *deepgramStreamAdapter) Finalize() error {
    return a.stream.Finalize()
}

func (a *deepgramStreamAdapter) Close() error {
    return a.stream.Close()
}

func (p *deepgramProvider) WithConfig(apiKey, model string) (Provider, error) {
    return &deepgramProvider{}, nil
}

func getConfigValue(key string, config map[string]any) string {
    if config != nil {
        if v, ok := config[key].(string); ok {
            return v
        }
    }
    // Fall back to environment variable
    return os.Getenv(key)
}
```

#### Groq Provider (New)

**File**: `pkg/voice/stt/groq_provider.go`

```go
package stt

import (
    "context"
    "encoding/json"
    "net/http"

    openai "github.com/sashabaranov/go-openai"
)

type groqProvider struct {
    apiKey string
    model  string
}

func (p *groqProvider) Name() string { return "groq" }

func (p *groqProvider) Capabilities() ProviderCapabilities {
    return ProviderCapabilities{
        Languages:        []string{"en", "es", "fr", "de", "it", "pt", "hi"},
        Models:           []string{"whisper-large-v3", "whisper-large-v3-turbo"},
        SupportsStreaming:    false, // Requires StreamAdapter
        SupportsDiarization:  false,
        SupportsMultilingual: true,
        MaxAudioDuration:     25 * 60, // 25 minutes
    }
}

func (p *groqProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
    // Groq uses OpenAI-compatible API
    client := openai.NewClientWithConfig(openai.ClientConfig{
        APIKey: p.apiKey,
        BaseURL: "https://api.groq.com/openai/v1",
        HTTPClient: &http.Client{},
    })

    // For non-streaming providers, we need a StreamAdapter
    adapter := &streamAdapter{
        provider:     "groq",
        client:       client,
        model:        p.model,
        language:     opts.Language,
        sampleRate:   opts.SampleRate,
        buffer:       make([]byte, 0),
    }

    return adapter, nil
}

func (p *groqProvider) WithConfig(apiKey, model string) (Provider, error) {
    if model == "" {
        model = "whisper-large-v3"
    }
    return &groqProvider{apiKey: apiKey, model: model}, nil
}
```

#### AssemblyAI Provider (New)

**File**: `pkg/voice/stt/assemblyai_provider.go`

```go
package stt

import (
    "context"
)

type assemblyAIProvider struct {
    apiKey string
}

func (p *assemblyAIProvider) Name() string { return "assemblyai" }

func (p *assemblyAIProvider) Capabilities() ProviderCapabilities {
    return ProviderCapabilities{
        Languages:        []string{"en", "es", "fr", "de", "it", "pt", "nl"},
        Models:           []string{"universal-3", "universal", "best"},
        SupportsStreaming:    true,
        SupportsDiarization:  true,
        SupportsMultilingual: false,
    }
}

func (p *assemblyAIProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
    // AssemblyAI streaming implementation
    apiKey := p.apiKey
    if apiKey == "" {
        apiKey = os.Getenv("ASSEMBLYAI_API_KEY")
    }

    return &assemblyAIStream{
        apiKey:     apiKey,
        model:      "universal-3",
        language:   opts.Language,
        diarization: opts.EnableDiarization,
    }, nil
}

func (p *assemblyAIProvider) WithConfig(apiKey, model string) (Provider, error) {
    if model == "" {
        model = "universal-3"
    }
    return &assemblyAIProvider{apiKey: apiKey}, nil
}
```

### 4. Integration with LiveKit

**Updated `cmd/picoclaw-livekit/main.go`**:

```go
func main() {
    // ...existing setup...

    // Initialize STT factory with database
    configDir := filepath.Dir(cfgPath)
    sttDBPath := filepath.Join(configDir, "stt_providers.db")

    sttFactory, err := stt.NewFactory(sttDBPath)
    if err != nil {
        logger.ErrorCF("livekit", "Failed to create STT factory", map[string]any{
            "error": err.Error(),
        })
        os.Exit(1)
    }

    logger.InfoCF("livekit", "STT factory initialized", map[string]any{
        "db_path":  sttDBPath,
        "providers": sttFactory.ListProviders(),
    })

    // Create RoomFactory with STT factory
    workerCfg := livekit.WorkerConfig{
        // ...existing config...
        RoomFactory: func(job *livekitproto.Job, assignment *livekitproto.JobAssignment, bridge *livekit.AgentBridge) (*livekit.RoomSession, error) {
            // Get active STT provider for this session
            sttProvider, err := sttFactory.GetActiveProvider()
            if err != nil {
                logger.WarnCF("livekit", "No STT provider available, using default", map[string]any{
                    "error": err.Error(),
                })
            }

            return livekit.NewRoomSession(livekit.RoomSessionConfig{
                // ...existing config...
                STT:             sttProvider,  // Changed from Deepgram to interface
                // ...
            })
        },
    }
    worker = livekit.NewWorker(workerCfg)

    // ...rest of main...
}
```

**Updated `pkg/livekit/room_session.go`**:

```go
import (
    // ...existing imports...
    "github.com/sipeed/picoclaw/pkg/voice/stt"
)

type RoomSessionConfig struct {
    // ...existing fields...
    STT    stt.Provider  // Changed from *deepgram.DeepgramTranscriber
    // ...
}

type RoomSession struct {
    // ...existing fields...
    stt    stt.Provider  // Changed from *deepgram.DeepgramTranscriber
    // ...
}

func (rs *RoomSession) handleTrackSubscribed(track *webrtc.TrackRemote, rp *lksdk.RemoteParticipant) {
    // ...existing code...

    // Get STT provider from session config
    if rs.stt == nil {
        logger.WarnC("livekit", "STT provider not configured")
        return
    }

    // Get provider capabilities
    caps := rs.stt.Capabilities()

    // Determine model and language
    model := "auto"
    language := rs.primaryLanguage

    // Validate language support
    if len(caps.Languages) > 0 && language != "" {
        supported := false
        for _, lang := range caps.Languages {
            if lang == language || lang == "auto" || lang == "multi" {
                supported = true
                break
            }
        }
        if !supported {
            logger.WarnCF("livekit", "Language not supported, using auto", map[string]any{
                "language": language,
                "provider": rs.stt.Name(),
            })
            language = "auto"
        }
    }

    // Open transcription stream with provider-specific options
    stream, err := rs.stt.OpenStream(rs.ctx, stt.StreamOptions{
        SampleRate:        16000,
        Channels:          1,
        Language:          language,
        Model:             model,
        InterimResults:    true,
        EndpointingMS:     800,
        EnableDiarization: false, // Enable based on use case
    })
    if err != nil {
        logger.ErrorCF("livekit", "Failed to open STT stream", map[string]any{
            "provider": rs.stt.Name(),
            "error":    err.Error(),
        })
        return
    }

    logger.InfoCF("livekit", "STT stream opened", map[string]any{
        "provider": rs.stt.Name(),
        "language": language,
    })

    // ...rest of existing code with stream...}
}
```

## Database Schema with Priority/Failover

```sql
CREATE TABLE IF NOT EXISTS stt_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_name TEXT NOT NULL UNIQUE,
    api_key TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    language TEXT,                         -- Default language (optional)
    sample_rate INTEGER DEFAULT 16000,
    is_active BOOLEAN NOT NULL DEFAULT 0,
    priority INTEGER DEFAULT 0,            -- Higher priority = preferred failover order
    config_json TEXT,                      -- Provider-specific JSON config
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_stt_active ON stt_providers(is_active);
CREATE INDEX IF NOT EXISTS idx_stt_priority ON stt_providers(priority DESC);

-- Default provider priorities (tier-based)
UPDATE stt_providers SET priority = 1 WHERE provider_name = 'deepgram';
UPDATE stt_providers SET priority = 2 WHERE provider_name = 'assemblyai';
UPDATE stt_providers SET priority = 3 WHERE provider_name = 'cartesia';
UPDATE stt_providers SET priority = 4 WHERE provider_name = 'elevenlabs';
UPDATE stt_providers SET priority = 5 WHERE provider_name = 'groq';
UPDATE stt_providers SET priority = 6 WHERE provider_name = 'openai';
UPDATE stt_providers SET priority = 7 WHERE provider_name = 'azure';
UPDATE stt_providers SET priority = 8 WHERE provider_name = 'google';
```

## Provider Selection Logic

### At Room Join
1. Query `stt_providers` for `is_active = 1`
2. If multiple active (shouldn't happen), select highest priority
3. If none active, default to Deepgram (if API key configured)
4. If Deepgram key missing, fall back to next available provider by priority

### Failover During Session
```go
// In RoomSession, handle provider failure
func (rs *RoomSession) handleSTTFailure(err error) {
    if rs.sttFactory == nil {
        return
    }

    // Get next provider by priority
    nextProvider, failoverErr := rs.sttFactory.GetNextProvider(rs.currentProvider.Name())

    if failoverErr != nil {
        logger.ErrorCF("livekit", "No failover providers available", map[string]any{
            "error": failoverErr.Error(),
        })
        return
    }

    logger.InfoCF("livekit", "Failing over to next provider", map[string]any{
        "from": rs.currentProvider.Name(),
        "to":   nextProvider.Name(),
    })

    // Switch providers for next utterance
    rs.currentProvider = nextProvider
}
```

## Configuration Examples

### Set Active Provider via CLI
```bash
# Set Groq as active provider
curl -X POST http://localhost:8080/api/stt/active \
  -H "Content-Type: application/json" \
  -d '{"provider": "groq", "api_key": "gsk_...", "model": "whisper-large-v3"}'

# List available providers
curl http://localhost:8080/api/stt/providers

# Update provider priority
curl -X PATCH http://localhost:8080/api/stt/priority \
  -H "Content-Type: application/json" \
  -d '{"provider": "deepgram", "priority": 10}'
```

### Initialize with Environment Variables
```go
// In main.go, seed providers from env vars
if apiKey := os.Getenv("DEEPGRAM_API_KEY"); apiKey != "" {
    sttFactory.UpdateProviderConfig("deepgram", apiKey, "nova-2", true, 1)
}

if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
    sttFactory.UpdateProviderConfig("groq", apiKey, "whisper-large-v3", false, 5)
}

if apiKey := os.Getenv("ASSEMBLYAI_API_KEY"); apiKey != "" {
    sttFactory.UpdateProviderConfig("assemblyai", apiKey, "universal-3", false, 2)
}
```

## Testing Strategy

### Unit Tests
- `pkg/voice/stt/factory_test.go` - Provider selection, DB operations, failover
- `pkg/voice/stt/deepgram_provider_test.go` - Interface compliance
- `pkg/voice/stt/groq_provider_test.go` - API integration
- `pkg/voice/stt/assemblyai_provider_test.go` - Streaming implementation

### Integration Tests
- Room join with active provider selection
- Provider failover during active session
- No provider configured fallback
- Multiple providers with same priority (round-robin)
- Database migration from single to multi-provider

### End-to-End Tests
1. Start worker with Deepgram active
2. Connect client, verify transcription works
3. Switch active provider to Groq via API
4. Connect new client, verify Groq is used
5. Simulate Groq failure, verify failover to next provider

## Implementation Status

**Completed (April 7, 2026)**

The following components have been implemented and merged into the `feature/livekit-approach-c` branch:

### Core Infrastructure (~100% Complete)
- ✅ `pkg/voice/stt/provider.go` - Core STT provider interface with `Provider`, `TranscriptionStream`, `TranscriptEvent` types
- ✅ `pkg/voice/stt/factory.go` - SQLite-backed factory with provider registration and selection
- ✅ `pkg/voice/stt/stt_utils.go` - Shared utility functions (WAV file creation, binary encoding)
- ✅ `pkg/config/config.go` - STT configuration struct added to `LiveKitServiceConfig`
- ✅ `pkg/livekit/room_session.go` - Updated to use generic `stt.Provider` interface
- ✅ `pkg/livekit/audio_pipeline.go` - Updated to accept `stt.TranscriptionStream` interface
- ✅ `cmd/picoclaw-livekit/main.go` - Integrated STT factory with environment variable seeding

### Implemented Providers

#### Deepgram (Tier 1 Support)
- **File**: `pkg/voice/stt/deepgram_adapter.go`
- **Models**: nova-2, nova-3, flux
- **Features**: Streaming, diarization, multilingual (44 languages)
- **Adapter**: Wraps existing streaming transcriber in generic interface

#### Groq Whisper (Tier 2 Support)
- **File**: `pkg/voice/stt/groq_provider.go`
- **Models**: whisper-large-v3, whisper-large-v3-turbo
- **Features**: Multilingual, ultra-fast inference, no diarization
- **Adapter**: StreamAdapter pattern for non-streaming OpenAI-compatible API
- **Dependencies**: `github.com/sashabaranov/go-openai`

#### AssemblyAI (Tier 1 Support)
- **File**: `pkg/voice/stt/assemblyai_provider.go`
- **Models**: universal, universal_pro
- **Features**: REST API with polling, diarization support
- **Adapter**: StreamAdapter with audio buffering and endpoint detection
- **Dependencies**: Standard library only (no SDK required)

### Provider Capabilities Matrix

| Provider       | Streaming | Diarization | Multilingual | Models                              |
|----------------|-----------|-------------|--------------|-------------------------------------|
| Deepgram       | ✅        | ✅          | ✅ (44 lang) | nova-2, nova-3, flux                |
| Groq           | ❌        | ❌          | ✅           | whisper-large-v3, whisper-large-v3-turbo |
| AssemblyAI     | ❌        | ✅          | ❌ (EN only) | universal, universal_pro            |

### StreamAdapter Pattern

Implemented for non-streaming providers (Groq, AssemblyAI):
1. Buffers PCM audio in memory
2. Detects speech endpoints using configurable VAD threshold
3. Converts buffered audio to WAV format
4. Calls provider's REST API
5. Returns transcription via channel to match streaming interface

### Database Schema

SQLite table `stt_providers`:
```sql
CREATE TABLE stt_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_name TEXT NOT NULL UNIQUE,
    api_key TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    language TEXT,
    sample_rate INTEGER DEFAULT 16000,
    is_active BOOLEAN NOT NULL DEFAULT 0,
    priority INTEGER DEFAULT 0,
    config_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

Default seed providers (priority = failover order):
- deepgram (priority 1, nova-2 model)
- groq (priority 5, whisper-large-v3 model)
- assemblyai (priority 2, universal model)

### Runtime Configuration

Provider selection via SQLite:
```sql
-- Activate Groq provider
UPDATE stt_providers SET is_active = 1 WHERE provider_name = 'groq';
UPDATE stt_providers SET is_active = 0 WHERE provider_name != 'groq';

-- Set priority for failover
UPDATE stt_providers SET priority = 10 WHERE provider_name = 'assemblyai';
```

Environment variable seeding (on process start):
```bash
export DEEPGRAM_API_KEY="sk_..."
export GROQ_API_KEY="gsk_..."
export ASSEMBLYAI_API_KEY="your_key..."
```

### Remaining Work

**Admin API (Not Implemented)**:
- REST endpoint to switch active provider (PATCH /api/stt/active)
- GET /api/stt/providers - list all providers
- POST /api/stt/providers - add new provider
- DELETE /api/stt/providers/:name - remove provider

**Additional Providers**:
- Cartesia Ink Whisper
- ElevenLabs Scribe v2
- OpenAI Whisper API
- Azure AI Speech
- Google Cloud Speech-to-Text
- AWS Transcribe
- Other providers from Tier 1-3 list

**Advanced Features**:
- Automatic provider failover on errors
- Per-provider metrics and monitoring
- Language-based provider selection
- Cost-based routing

---

## Migration Plan

**Phase 1: Abstraction Layer (✅ Done)**
- Create `pkg/voice/stt/provider.go` interface
- Refactor existing Deepgram code to implement interface
- No functional changes yet

**Phase 2: Database Integration (✅ Done)**
- Add SQLite schema and factory
- Implement provider registration
- Add environment variable seeding

**Phase 3: Additional Providers (✅ Done for Groq & AssemblyAI)**
- ✅ Implement Groq provider (high priority, popular)
- ✅ Implement AssemblyAI provider
- ⬜ Implement OpenAI Whisper provider
- ⬜ Implement Cartesia provider
- ⬜ Implement ElevenLabs provider

**Phase 4: LiveKit Integration (✅ Done)**
- Update `main.go` to use STT factory
- Update `room_session.go` to use provider interface
- Add provider selection logging

**Phase 5: Advanced Features (⬜ Not Started)**
- Provider failover during sessions
- Round-robin load balancing for same-priority providers
- Per-room provider selection (based on metadata)
- Admin API for provider management
- Monitoring and metrics dashboard

**Phase 5: Advanced Features (Week 6+)**
- Provider failover during sessions
- Round-robin load balancing for same-priority providers
- Per-room provider selection (based on metadata)
- Admin UI for provider management

## Security Considerations

- API keys stored encrypted via existing `credential` package
- Provider configs in `.security.yml` pattern
- No provider credentials in logs
- Rate limiting on provider switching API
- Validation of provider names (prevent injection)

## Error Handling

```go
// Provider not found
if err == stt.ErrProviderNotFound {
    logger.WarnCF("livekit", "Requested provider not available, using default", map[string]any{
        "requested": providerName,
        "defaulting": "deepgram",
    })
}

// API key not configured
if err == stt.ErrAPIKeyMissing {
    return nil, fmt.Errorf("provider %s: API key not configured", providerName)
}

// Provider capacity exceeded
if err == stt.ErrCapacityExceeded {
    // Trigger failover to next provider
    return rs.failoverToNextProvider()
}
```

## Monitoring & Observability

```go
// Track provider usage metrics
type STTMetrics struct {
    Provider        string
    TotalRequests   int64
    FailedRequests  int64
    AvgLatency      time.Duration
    TotalAudioSeconds float64
    ActiveSessions  int
}

// Expose via metrics endpoint
func (f *Factory) GetMetrics() map[string]STTMetrics {
    // Return per-provider metrics
}
```

## References

- LiveKit STT docs: https://docs.livekit.io/agents/models/stt/
- Deepgram SDK: `pkg/voice/deepgram/`
- TTS factory pattern: `pkg/voice/tts/factory.go`
- AssemblyAI API: https://www.assemblyai.com/docs
- Groq API: https://console.groq.com/docs
- Azure Speech: https://learn.microsoft.com/azure/cognitive-services/speech-service/
