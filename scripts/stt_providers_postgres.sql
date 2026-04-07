-- PostgreSQL STT Providers Schema
-- Run this on your Supabase database to set up STT provider configuration

-- Create the table
CREATE TABLE IF NOT EXISTS stt_providers (
    id SERIAL PRIMARY KEY,
    provider_name TEXT NOT NULL UNIQUE,
    api_key TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    language TEXT,
    sample_rate INTEGER DEFAULT 16000,
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    priority INTEGER DEFAULT 0,
    config_json JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_stt_active ON stt_providers(is_active);
CREATE INDEX IF NOT EXISTS idx_stt_priority ON stt_providers(priority DESC);

-- Create updated_at trigger function
CREATE OR REPLACE FUNCTION update_stt_providers_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Create trigger
DROP TRIGGER IF EXISTS stt_providers_updated_at ON stt_providers;
CREATE TRIGGER stt_providers_updated_at
    BEFORE UPDATE ON stt_providers
    FOR EACH ROW
    EXECUTE FUNCTION update_stt_providers_updated_at();

-- Insert default providers (will not overwrite existing ones)
INSERT INTO stt_providers (provider_name, api_key, model, is_active, priority)
VALUES
    ('deepgram', '', 'nova-2', FALSE, 1),
    ('assemblyai', '', 'universal', FALSE, 2),
    ('groq', '', 'whisper-large-v3', FALSE, 5),
    ('openai', '', 'whisper-1', FALSE, 6),
    ('cartesia', '', 'ink-whisper', FALSE, 7),
    ('elevenlabs', '', 'scribe_v2', FALSE, 8),
    ('azure', '', 'latest', FALSE, 9),
    ('google', '', 'latest_long', FALSE, 10),
    ('aws', '', 'Conversational', FALSE, 11),
    ('soniox', '', 'standard_v2', FALSE, 12),
    ('speechmatics', '', '2.0-a', FALSE, 13),
    ('gladia', '', 'gladia-2', FALSE, 14)
ON CONFLICT (provider_name) DO NOTHING;

-- Useful queries:

-- View all providers:
-- SELECT provider_name, model, priority, is_active FROM stt_providers ORDER BY priority DESC;

-- Activate Deepgram:
-- UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'deepgram';
-- UPDATE stt_providers SET is_active = FALSE WHERE provider_name != 'deepgram';

-- Set API key for a provider:
-- UPDATE stt_providers SET api_key = 'your-api-key-here' WHERE provider_name = 'deepgram';

-- Check current active provider:
-- SELECT * FROM stt_providers WHERE is_active = TRUE;

-- RLS Policy (if you want to restrict access):
-- ALTER TABLE stt_providers ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY "Allow read access to all authenticated users" ON stt_providers FOR SELECT TO authenticated USING (true);
-- CREATE POLICY "Allow write access to authenticated users" ON stt_providers FOR ALL TO authenticated USING (true) WITH CHECK (true);
