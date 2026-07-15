-- PostgreSQL TTS Providers Schema
-- Run this on the Manager/Supabase database to set up LiveKit TTS provider configuration.

CREATE TABLE IF NOT EXISTS tts_providers (
    id SERIAL PRIMARY KEY,
    provider_name TEXT NOT NULL UNIQUE,
    api_key TEXT NOT NULL DEFAULT '',
    voice_id TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    output_format TEXT NOT NULL DEFAULT 'pcm_24000',
    sample_rate_hz INTEGER NOT NULL DEFAULT 24000,
    temperature DOUBLE PRECISION,
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    priority INTEGER NOT NULL DEFAULT 0,
    config_json JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tts_active ON tts_providers(is_active);
CREATE INDEX IF NOT EXISTS idx_tts_priority ON tts_providers(priority DESC);

CREATE OR REPLACE FUNCTION update_tts_providers_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tts_providers_updated_at ON tts_providers;
CREATE TRIGGER tts_providers_updated_at
    BEFORE UPDATE ON tts_providers
    FOR EACH ROW
    EXECUTE FUNCTION update_tts_providers_updated_at();

INSERT INTO tts_providers (
    provider_name,
    api_key,
    voice_id,
    model_id,
    output_format,
    sample_rate_hz,
    is_active,
    priority
)
VALUES
    ('elevenlabs', '', '', 'eleven_flash_v2_5', 'pcm_24000', 24000, FALSE, 10),
    ('cartesia', '', '', 'sonic-3', 'pcm_24000', 24000, FALSE, 20),
    ('deepgram', '', '', 'aura-2-asteria-en', 'pcm_24000', 24000, FALSE, 30),
    -- Sarvam bulbul: language_code is resolved per session from the child's
    -- language (not stored here). sample_rate_hz feeds the request sample_rate.
    ('sarvam', '', 'pooja', 'bulbul:v3', 'pcm_24000', 24000, FALSE, 40),
    -- Edge TTS: free, keyless, developer path. voice_id is an Edge voice name.
    ('edge', '', 'en-US-AnaNeural', '', 'pcm_24000', 24000, FALSE, 50),
    -- Azure Speech: region/endpoint + key come from the worker env
    -- (AZURE_SPEECH_REGION / AZURE_SPEECH_KEY); api_key here is an optional override.
    ('azure', '', 'en-US-AnaNeural', '', 'pcm_24000', 24000, FALSE, 60)
ON CONFLICT (provider_name) DO NOTHING;

-- Activate Deepgram Aura-2 TTS:
-- UPDATE tts_providers SET is_active = FALSE;
-- UPDATE tts_providers
-- SET is_active = TRUE,
--     api_key = 'your-deepgram-api-key',
--     model_id = 'aura-2-asteria-en',
--     output_format = 'pcm_24000',
--     sample_rate_hz = 24000
-- WHERE provider_name = 'deepgram';

-- Manager API should expose the active row as:
-- {
--   "provider": "deepgram",
--   "voice_id": "",
--   "model_id": "aura-2-asteria-en",
--   "output_format": "pcm_24000",
--   "sample_rate_hz": 24000,
--   "api_key": "your-deepgram-api-key"
-- }
