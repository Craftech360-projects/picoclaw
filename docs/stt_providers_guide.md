# STT Providers Guide

Complete guide for configuring and managing Speech-to-Text providers in PicoClaw LiveKit Voice Agent.

## Overview

The system supports **12 STT providers** across three tiers, all configurable via SQLite database with runtime provider switching.

## Provider Tiers

### Tier 1: LiveKit Inference (Managed Quality)
- **Deepgram** - Nova-2, Nova-3, Flux (streaming, diarization, 44 languages)
- **AssemblyAI** - Universal, Universal Pro (diarization, English)
- **Cartesia** - Ink Whisper (100+ languages)
- **ElevenLabs** - Scribe v2 (190 languages)
- **Gladia** - Gladia-2 (real-time, diarization)

### Tier 2: Major Cloud APIs
- **OpenAI** - Whisper-1 (multilingual)
- **Groq** - Whisper-large-v3, whisper-large-v3-turbo (ultra-fast)
- **Azure AI Speech** - Latest, Baseline, Conversation (diarization)
- **Google Cloud Speech** - latest_long, latest_short, phone_call, video
- **AWS Transcribe** - Conversational, Voicemail, PhoneCall

### Tier 3: Specialized Providers
- **Soniox** - standard_v2, premium_v1_short (high-accuracy, diarization)
- **Speechmatics** - 2.0-a, 2.1-b (enterprise, diarization, multilingual)

## Provider Capabilities Matrix

| Provider       | Streaming | Diarization | Multilingual | Languages                    | Models                              |
|----------------|-----------|-------------|--------------|------------------------------|-------------------------------------|
| Deepgram       | ✅        | ✅          | ✅           | 44 languages                 | nova-2, nova-3, flux                |
| AssemblyAI     | ❌        | ✅          | ❌           | English                      | universal, universal_pro            |
| Cartesia       | ❌        | ❌          | ✅           | 100+ languages (auto)        | ink-whisper                         |
| ElevenLabs     | ❌        | ❌          | ✅           | 190 languages                | scribe_v2                           |
| Gladia         | ✅        | ✅          | ✅           | Auto-detect                  | gladia-2                            |
| OpenAI         | ❌        | ❌          | ✅           | 100+ languages               | whisper-1                           |
| Groq           | ❌        | ❌          | ✅           | 100+ languages               | whisper-large-v3, whisper-large-v3-turbo |
| Azure          | ❌        | ✅          | ✅           | 50+ languages                | latest, baseline, conversation      |
| Google         | ❌        | ✅          | ✅           | 125+ languages               | latest_long, latest_short           |
| AWS            | ❌        | ✅          | ❌           | English, Spanish, French     | Conversational, Voicemail           |
| Soniox         | ❌        | ✅          | ❌           | English                      | standard_v2, premium_v1_short       |
| Speechmatics   | ❌        | ✅          | ✅           | 30+ languages                | 2.0-a, 2.1-b                        |

## Database Setup

### Initialize Database

```bash
# Using the initialization script
go run scripts/init_stt_db.go ~/.picoclaw/stt_providers.db

# Or use SQLite directly
sqlite3 ~/.picoclaw/stt_providers.db
```

### Default Configuration

All providers are pre-registered with default settings:

```sql
SELECT provider_name, model, priority FROM stt_providers ORDER BY priority DESC;
```

Results in priority order (highest first for failover):
1. Deepgram (priority 1)
2. AssemblyAI (priority 2)
3. Groq (priority 5)
4. OpenAI (priority 6)
5. Cartesia (priority 7)
6. ElevenLabs (priority 8)
7. Azure (priority 9)
8. Google (priority 10)
9. AWS (priority 11)
10. Soniox (priority 12)
11. Speechmatics (priority 13)
12. Gladia (priority 14)

## Configuration

### Environment Variables

Set API keys via environment variables (automatically seeded on startup):

```bash
# Tier 1
export DEEPGRAM_API_KEY="sk_..."
export ASSEMBLYAI_API_KEY="your_key..."
export CARTESIA_API_KEY="your_key..."
export ELEVENLABS_API_KEY="your_key..."
export GLADIA_API_KEY="your_key..."

# Tier 2
export OPENAI_API_KEY="sk-..."
export GROQ_API_KEY="gsk_..."
export AZURE_SPEECH_KEY="..."
export AZURE_SPEECH_REGION="eastus"  # or any Azure region
export GOOGLE_CLOUD_API_KEY="AIza..."
export AWS_ACCESS_KEY_ID="AKIA..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_REGION="us-east-1"

# Tier 3
export SONIOX_API_KEY="your_key..."
export SPEECHMATICS_API_KEY="your_key..."
```

### Activate Provider via SQL

```sql
-- Activate Deepgram
UPDATE stt_providers SET is_active = 1 WHERE provider_name = 'deepgram';
UPDATE stt_providers SET is_active = 0 WHERE provider_name != 'deepgram';

-- Activate Groq
UPDATE stt_providers SET is_active = 1 WHERE provider_name = 'groq';
UPDATE stt_providers SET is_active = 0 WHERE provider_name != 'groq';
```

### Using CLI Manager

```bash
# List all providers
./scripts/stt_provider_manager list --db ~/.picoclaw/stt_providers.db

# Activate a provider
./scripts/stt_provider_manager activate deepgram --db ~/.picoclaw/stt_providers.db

# Set API key
./scripts/stt_provider_manager set-key deepgram sk-your-key --db ~/.picoclaw/stt_providers.db

# Check capabilities
./scripts/stt_provider_manager capabilities groq --db ~/.picoclaw/stt_providers.db

# Show current status
./scripts/stt_provider_manager status --db ~/.picoclaw/stt_providers.db
```

### Direct SQLite Commands

```bash
sqlite3 ~/.picoclaw/stt_providers.db

-- View all providers
SELECT * FROM stt_providers ORDER BY priority DESC;

-- View active provider
SELECT * FROM stt_providers WHERE is_active = 1;

-- Update API key
UPDATE stt_providers SET api_key = 'your-key' WHERE provider_name = 'deepgram';

-- Change priority (for failover order)
UPDATE stt_providers SET priority = 100 WHERE provider_name = 'groq';

-- Add custom provider (if needed)
INSERT INTO stt_providers (provider_name, api_key, model, is_active, priority)
VALUES ('custom', 'key', 'model', 0, 15);
```

## Runtime Behavior

### Provider Selection

1. Agent starts → reads active provider from database
2. If no provider active → defaults to Deepgram (nova-2)
3. Missing API key → logs warning, still attempts to use provider
4. Provider failure → **manual intervention required** (no auto-failover yet)

### Switching Providers

Provider switching happens **at room join time**:
- New connections use the new active provider
- Existing sessions continue with their original provider
- No interruption to ongoing calls

### Logging

Provider selection is logged:

```
INFO  STT factory initialized     db_path=/home/user/.picoclaw/stt_providers.db providers=[deepgram, groq, ...]
INFO  Using STT provider          provider=deepgram
INFO  STT stream opened           provider=deepgram language=en
```

## Switching Examples

### Example 1: Switch from Deepgram to Groq

```bash
# 1. Set Groq API key (if not already set via env)
./scripts/stt_provider_manager set-key groq gsk_your_key

# 2. Activate Groq
./scripts/stt_provider_manager activate groq

# 3. New rooms will use Groq immediately
# Check logs: "Using STT provider provider=groq"
```

### Example 2: Test AssemblyAI

```bash
# Activate AssemblyAI
./scripts/stt_provider_manager activate assemblyai

# Set API key
./scripts/stt_provider_manager set-key assemblyai your_assemblyai_key

# Verify
./scripts/stt_provider_manager status
```

### Example 3: Multi-language Support

For Hindi/Indian languages, switch to a multilingual provider:

```bash
# Activate OpenAI Whisper (supports 100+ languages)
./scripts/stt_provider_manager activate openai

# Or ElevenLabs Scribe v2 (190 languages)
./scripts/stt_provider_manager activate elevenlabs
```

## Troubleshooting

### Provider Not Found Error

```
Error: provider "xyz" not registered
```

**Solution:** Check available providers:
```bash
./scripts/stt_provider_manager list
```

### API Key Missing

```
Error: deepgram: API key not configured
```

**Solution:** Set environment variable or update database:
```bash
export DEEPGRAM_API_KEY="your-key"
# OR
./scripts/stt_provider_manager set-key deepgram your-key
```

### Transcription Fails

Check provider logs for specific errors:
```bash
# View provider capabilities
./scripts/stt_provider_manager capabilities <provider-name>

# Check if provider supports your language
SELECT model, language FROM stt_providers WHERE provider_name = 'deepgram';
```

### Database Issues

```bash
# Reinitialize database
rm ~/.picoclaw/stt_providers.db
go run scripts/init_stt_db.go ~/.picoclaw/stt_providers.db
```

## Advanced Usage

### Custom Provider Configuration

Add provider-specific settings via `config_json` field:

```sql
UPDATE stt_providers
SET config_json = '{"endpointing_ms": 800, "enable_diarization": true}'
WHERE provider_name = 'deepgram';
```

### Provider Failover Testing

Test failover by deactivating current provider:

```sql
-- Force fallback to next priority
UPDATE stt_providers SET is_active = 0;
-- System will default to Deepgram
```

### Per-Room Provider Selection

Future enhancement: read provider from room metadata:

```json
{
  "child_profile": {...},
  "stt_provider": "groq",
  "stt_language": "hi"
}
```

## Monitoring

Track provider usage via logs:

```bash
# Monitor provider selection
grep "Using STT provider" logs/picoclaw.log

# Track transcription errors
grep "STT.*failed\|STT.*error" logs/picoclaw.log

# Count provider usage
grep "Using STT provider" logs/picoclaw.log | sort | uniq -c
```

## Best Practices

1. **Test before switching**: Use test environment before changing production provider
2. **Monitor costs**: Different providers have different pricing models
3. **Language support**: Verify provider supports your target language
4. **Latency requirements**: Groq is fastest, cloud APIs have higher latency
5. **Quality tradeoffs**: Tier 1 providers generally have better quality
6. **Backup provider**: Keep a secondary provider configured for emergencies
7. **API key security**: Use environment variables, don't commit keys to database

## Adding New Providers

To add a custom provider:

1. Implement `stt.Provider` interface
2. Register in `factory.go:registerBuiltInProviders()`
3. Add to database schema in `factory.go:initDB()`
4. Rebuild: `go build ./cmd/picoclaw-livekit`

Example provider implementation in `pkg/voice/stt/custom_provider.go`.

## Support

For provider-specific issues, check:
- Provider documentation: https://docs.livekit.io/agents/models/stt/
- API status pages for each provider
- PicoClaw issues: https://github.com/sipeed/picoclaw/issues
