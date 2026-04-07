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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("init STT DB: %w", err)
	}

	f := &Factory{
		dbPath:    dbPath,
		db:        db,
		providers: make(map[string]Provider),
	}

	if err := f.initDB(); err != nil {
		return nil, fmt.Errorf("init STT schema: %w", err)
	}

	f.registerBuiltInProviders()

	return f, nil
}

// Close closes the underlying database connection.
func (f *Factory) Close() error {
	return f.db.Close()
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
		// Default to deepgram if no provider is active
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

func (f *Factory) initDB() error {
	schema := `
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

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('deepgram', '', 'nova-2', 0, 1);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('groq', '', 'whisper-large-v3', 0, 5);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('assemblyai', '', 'universal', 0, 2);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('openai', '', 'whisper-1', 0, 6);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('cartesia', '', 'ink-whisper', 0, 7);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('elevenlabs', '', 'scribe_v2', 0, 8);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('azure', '', 'latest', 0, 9);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('google', '', 'latest_long', 0, 10);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('aws', '', 'Conversational', 0, 11);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('soniox', '', 'standard_v2', 0, 12);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('speechmatics', '', '2.0-a', 0, 13);

		INSERT OR IGNORE INTO stt_providers (provider_name, api_key, model, is_active, priority)
		VALUES ('gladia', '', 'gladia-2', 0, 14);
	`

	_, err := f.db.Exec(schema)
	return err
}

func (f *Factory) registerBuiltInProviders() {
	f.providers["deepgram"] = &deepgramProvider{}
	f.providers["groq"] = NewGroqProvider("", "")
	f.providers["assemblyai"] = NewAssemblyAIProvider("", "")
	f.providers["openai"] = NewOpenAIProvider("", "")
	f.providers["cartesia"] = NewCartesiaProvider("", "")
	f.providers["elevenlabs"] = NewElevenLabsProvider("", "")
	f.providers["azure"] = NewAzureProvider("", "", "", "")
	f.providers["google"] = NewGoogleProvider("", "", "", false)
	f.providers["aws"] = NewAWSProvider("", "", "", "", "")
}
