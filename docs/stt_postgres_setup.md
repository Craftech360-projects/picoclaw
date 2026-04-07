# STT Provider PostgreSQL Setup Guide

This guide explains how to set up and configure the STT provider database using PostgreSQL (Supabase).

## Overview

The STT provider configuration is stored in a PostgreSQL database table, allowing:
- Centralized configuration across multiple agent instances
- Runtime provider switching without restarting
- API key management in a secure database
- Easy management via SQL queries or admin tools

## Database Setup

### 1. Connect to Your Supabase Database

You already have a Supabase database configured:

```
PostgreSQL URL: postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres
```

### 2. Create the STT Providers Table

Run the SQL schema on your Supabase database:

```bash
# Option 1: Use Supabase Dashboard SQL Editor
# - Go to SQL Editor in your Supabase dashboard
# - Copy contents of scripts/stt_providers_postgres.sql
# - Paste and run

# Option 2: Use psql command line
psql "postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres" -f scripts/stt_providers_postgres.sql

# Option 3: Use any PostgreSQL client
```

### 3. Verify Table Creation

```sql
-- Check table exists
SELECT table_name FROM information_schema.tables
WHERE table_schema = 'public' AND table_name = 'stt_providers';

-- View default providers
SELECT provider_name, model, priority FROM stt_providers ORDER BY priority DESC;
```

Expected output: 12 providers registered (deepgram, assemblyai, groq, etc.)

## Configuration

### Environment Variables

Set the database connection in your environment:

```bash
# Option 1: Direct Supabase URL (recommended)
export STT_DATABASE_URL="postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres"

# Option 2: Use existing DIRECT_URL
export DIRECT_URL="postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres"

# Option 3: Set in config.json
{
  "livekit_service": {
    "stt": {
      "database_url": "postgresql://..."
    }
  }
}
```

### Start the Agent

```bash
# Start with PostgreSQL backend
export STT_DATABASE_URL="postgresql://..."
./picoclaw-livekit --agent-name=my-agent --config ~/.picoclaw/config.json

# Logs will show:
# INFO STT factory initialized  db_url=postgresql://... providers=[deepgram, groq, ...]
```

## Managing Providers

### View All Providers

```sql
SELECT
    provider_name,
    model,
    is_active,
    priority,
    language,
    CASE
        WHEN api_key != '' THEN 'Configured'
        ELSE 'Not Set'
    END as api_key_status
FROM stt_providers
ORDER BY priority DESC;
```

### Activate a Provider

```sql
-- Activate Deepgram (set others to inactive)
UPDATE stt_providers SET is_active = FALSE;
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'deepgram';

-- Verify active provider
SELECT provider_name, model FROM stt_providers WHERE is_active = TRUE;
```

### Set API Keys

```sql
-- Set API key for specific provider
UPDATE stt_providers
SET api_key = 'sk-your-deepgram-key'
WHERE provider_name = 'deepgram';

-- Set multiple API keys
UPDATE stt_providers SET api_key = 'gsk-your-groq-key' WHERE provider_name = 'groq';
UPDATE stt_providers SET api_key = 'your-openai-key' WHERE provider_name = 'openai';
```

### Provider Switching Example

```sql
-- Switch from Deepgram to Groq
BEGIN;
  UPDATE stt_providers SET is_active = FALSE;
  UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'groq';
COMMIT;

-- New agent sessions will use Groq
-- Existing sessions continue with their current provider
```

### Provider Failover Testing

```sql
-- Simulate provider failure by deactivating all
UPDATE stt_providers SET is_active = FALSE;

-- System falls back to Deepgram (hardcoded default)
```

## Monitoring & Diagnostics

### Check Current Active Provider

```sql
SELECT
    provider_name,
    model,
    priority,
    created_at,
    updated_at
FROM stt_providers
WHERE is_active = TRUE;
```

### Provider Usage Statistics

```sql
-- Count providers by status
SELECT is_active, COUNT(*) as count
FROM stt_providers
GROUP BY is_active;

-- View recently updated providers
SELECT provider_name, updated_at
FROM stt_providers
ORDER BY updated_at DESC
LIMIT 5;
```

### Audit Trail

```sql
-- Track when providers were modified
SELECT
    provider_name,
    updated_at,
    is_active,
    priority
FROM stt_providers
ORDER BY updated_at DESC;
```

## Advanced Configuration

### Provider-Specific Settings

Use the `config_json` field for provider-specific options:

```sql
-- Configure Deepgram with custom settings
UPDATE stt_providers
SET config_json = '{
  "endpointing_ms": 800,
  "punctuate": true,
  "profanity_filter": false,
  "redact": [],
  "enable_diarization": true
}'
WHERE provider_name = 'deepgram';

-- Configure Groq with specific model
UPDATE stt_providers
SET config_json = '{
  "temperature": 0,
  "response_format": "json"
}'
WHERE provider_name = 'groq';
```

### Custom Provider Priority

```sql
-- Reorder providers for your use case
UPDATE stt_providers SET priority = 100 WHERE provider_name = 'groq';  -- High priority
UPDATE stt_providers SET priority = 50 WHERE provider_name = 'openai';
UPDATE stt_providers SET priority = 1 WHERE provider_name = 'deepgram'; -- Low priority

-- Now Groq will be tried first (when implementing failover)
```

### Multi-Language Support

```sql
-- Set language preferences per provider
UPDATE stt_providers SET language = 'en' WHERE provider_name = 'deepgram';
UPDATE stt_providers SET language = 'hi' WHERE provider_name = 'openai';  -- Hindi
UPDATE stt_providers SET language = 'es' WHERE provider_name = 'azure';   -- Spanish
```

## Troubleshooting

### Connection Issues

```bash
# Test database connection
psql "postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres" -c "SELECT 1"

# Check environment variable
echo $STT_DATABASE_URL

# Test with direct URL
export DIRECT_URL="postgresql://..."
./picoclaw-livekit --agent-name=test
```

### Table Not Found

```sql
-- Recreate table
\i scripts/stt_providers_postgres.sql

-- Verify
\d stt_providers
```

### Provider Not Active

```sql
-- Check if any provider is active
SELECT COUNT(*) FROM stt_providers WHERE is_active = TRUE;

-- If zero, system defaults to Deepgram
-- Activate a provider:
UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'deepgram';
```

### API Key Not Working

```sql
-- Verify API key is set
SELECT provider_name,
       CASE WHEN api_key != '' THEN '✓ Set' ELSE '✗ Missing' END as status
FROM stt_providers;

-- Update key
UPDATE stt_providers SET api_key = 'new-key-here' WHERE provider_name = 'groq';
```

## Security Best Practices

### 1. Use Environment Variables for Credentials

```bash
# Don't hardcode in SQL or config
export STT_DATABASE_URL="postgresql://user:password@host:5432/db"

# Agent reads from environment
./picoclaw-livekit --agent-name=prod
```

### 2. Row Level Security (RLS)

If using Supabase with multiple tenants:

```sql
-- Enable RLS
ALTER TABLE stt_providers ENABLE ROW LEVEL SECURITY;

-- Create policy for specific agent/room
CREATE POLICY "Agents can read stt providers"
ON stt_providers FOR SELECT
TO authenticated
USING (true);

CREATE POLICY "Admins can update stt providers"
ON stt_providers FOR UPDATE
TO authenticated
USING (auth.jwt() ->> 'role' = 'admin');
```

### 3. Encrypt Sensitive Data

For production, consider encrypting API keys:

```sql
-- Use pgcrypto extension for encryption
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Store encrypted API keys
UPDATE stt_providers
SET api_key = pgp_sym_encrypt('your-api-key', 'encryption-key')
WHERE provider_name = 'deepgram';

-- Decrypt when reading
SELECT pgp_sym_decrypt(api_key::bytea, 'encryption-key')
FROM stt_providers
WHERE provider_name = 'deepgram';
```

## Backup & Restore

### Backup

```bash
# Export table data
pg_dump "postgresql://..." -t stt_providers --data-only > stt_providers_backup.sql

# Or export to CSV
COPY (SELECT * FROM stt_providers) TO '/tmp/stt_providers.csv' CSV HEADER;
```

### Restore

```bash
# Import from backup
psql "postgresql://..." -f stt_providers_backup.sql

# Or import from CSV
COPY stt_providers FROM '/tmp/stt_providers.csv' CSV HEADER;
```

## Integration with Multiple Agents

### Share Configuration Across Agents

All agent instances connect to the same database:

```bash
# Agent 1
export STT_DATABASE_URL="postgresql://..."
./picoclaw-livekit --agent-name=agent-1

# Agent 2 (uses same provider config)
export STT_DATABASE_URL="postgresql://..."
./picoclaw-livekit --agent-name=agent-2

# Switch provider once, affects all agents
psql "postgresql://..." -c "UPDATE stt_providers SET is_active = TRUE WHERE provider_name = 'groq';"
```

### Per-Agent Provider Selection

Future enhancement: read provider from job metadata:

```sql
-- Could implement agent-specific overrides
CREATE TABLE agent_stt_overrides (
    agent_id TEXT NOT NULL,
    provider_name TEXT NOT NULL,
    FOREIGN KEY (provider_name) REFERENCES stt_providers(provider_name)
);

-- Agent 1 uses Deepgram
INSERT INTO agent_stt_overrides (agent_id, provider_name)
VALUES ('agent-1', 'deepgram');

-- Agent 2 uses Groq
INSERT INTO agent_stt_overrides (agent_id, provider_name)
VALUES ('agent-2', 'groq');
```

## Migration from SQLite

If you were using SQLite and want to migrate:

```bash
# Export from SQLite
sqlite3 stt_providers.db ".dump stt_providers" > sqlite_dump.sql

# Convert SQLite syntax to PostgreSQL
sed -i 's/AUTOINCREMENT/SERIAL/g' sqlite_dump.sql
sed -i 's/DATETIME/TIMESTAMP/g' sqlite_dump.sql
sed -i "s/INSERT INTO/INSERT INTO/g" sqlite_dump.sql

# Import to PostgreSQL
psql "postgresql://..." -f sqlite_dump.sql
```

## Next Steps

1. ✅ Set up PostgreSQL table on Supabase
2. ✅ Configure environment variables
3. ✅ Test database connection
4. ✅ Set API keys for your preferred providers
5. ✅ Activate your default provider
6. ⬜ Implement admin API for runtime management
7. ⬜ Add provider failover logic
8. ⬜ Monitor provider usage and costs

## Support

For issues with:
- **Supabase**: Check Supabase dashboard logs
- **PostgreSQL**: Review PostgreSQL error logs
- **Connection**: Verify network/firewall settings
- **Providers**: Check provider API status pages
