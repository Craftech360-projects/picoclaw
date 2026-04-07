package stt

import (
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/lib/pq"
)

// Factory creates STT providers based on database configuration.
type Factory struct {
	dbURL     string
	db        *sql.DB
	providers map[string]Provider
	mu        sync.RWMutex
}

// NewFactory creates a new STT provider factory with PostgreSQL.
func NewFactory(dbURL string) (*Factory, error) {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("init STT DB: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping STT DB: %w", err)
	}

	f := &Factory{
		dbURL:     dbURL,
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
	if f.db != nil {
		return f.db.Close()
	}
	return nil
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
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(provider_name) DO UPDATE SET
			api_key = EXCLUDED.api_key,
			model = EXCLUDED.model,
			is_active = EXCLUDED.is_active,
			priority = EXCLUDED.priority,
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

// ProviderInfo holds detailed information about a provider
type ProviderInfo struct {
	Name     string
	Model    string
	Language string
	IsActive bool
	Priority int
	APIKey   string
}

// ListProvidersDetailed returns detailed provider information
func (f *Factory) ListProvidersDetailed() ([]ProviderInfo, error) {
	rows, err := f.db.Query(`
		SELECT provider_name, model, language, is_active, priority, api_key
		FROM stt_providers
		ORDER BY priority DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query providers: %w", err)
	}
	defer rows.Close()

	var providers []ProviderInfo
	for rows.Next() {
		var p ProviderInfo
		if err := rows.Scan(&p.Name, &p.Model, &p.Language, &p.IsActive, &p.Priority, &p.APIKey); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// ActivateProvider sets the specified provider as active
func (f *Factory) ActivateProvider(name string) error {
	// Check if provider exists
	var exists bool
	err := f.db.QueryRow("SELECT 1 FROM stt_providers WHERE provider_name = $1", name).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("provider %q not found", name)
	} else if err != nil {
		return fmt.Errorf("check provider: %w", err)
	}

	// Deactivate all, activate selected
	tx, err := f.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if _, err := tx.Exec("UPDATE stt_providers SET is_active = FALSE"); err != nil {
		tx.Rollback()
		return fmt.Errorf("deactivate all: %w", err)
	}

	if _, err := tx.Exec("UPDATE stt_providers SET is_active = TRUE WHERE provider_name = $1", name); err != nil {
		tx.Rollback()
		return fmt.Errorf("activate provider: %w", err)
	}

	return tx.Commit()
}

// SetProviderAPIKey updates the API key for a provider
func (f *Factory) SetProviderAPIKey(name, apiKey string) error {
	_, err := f.db.Exec("UPDATE stt_providers SET api_key = $1 WHERE provider_name = $2", apiKey, name)
	if err != nil {
		return fmt.Errorf("update API key: %w", err)
	}
	return nil
}

// GetProviderCapabilities returns capabilities for a specific provider
func (f *Factory) GetProviderCapabilities(name string) (ProviderCapabilities, error) {
	provider, ok := f.providers[name]
	if !ok {
		return ProviderCapabilities{}, fmt.Errorf("provider %q not registered", name)
	}
	return provider.Capabilities(), nil
}

func (f *Factory) initDB() error {
	schema := `
		CREATE TABLE IF NOT EXISTS stt_providers (
			id SERIAL PRIMARY KEY,
			provider_name TEXT NOT NULL UNIQUE,
			api_key TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			language TEXT,
			sample_rate INTEGER DEFAULT 16000,
			is_active BOOLEAN NOT NULL DEFAULT FALSE,
			priority INTEGER DEFAULT 0,
			config_json JSONB,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_stt_active ON stt_providers(is_active);
		CREATE INDEX IF NOT EXISTS idx_stt_priority ON stt_providers(priority DESC);

		-- Create updated_at trigger
		CREATE OR REPLACE FUNCTION update_stt_providers_updated_at()
		RETURNS TRIGGER AS $$
		BEGIN
			NEW.updated_at = CURRENT_TIMESTAMP;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;

		CREATE TRIGGER stt_providers_updated_at
		BEFORE UPDATE ON stt_providers
		FOR EACH ROW
		EXECUTE FUNCTION update_stt_providers_updated_at();

		-- Insert default providers (only if they don't exist)
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
