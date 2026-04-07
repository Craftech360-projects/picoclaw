package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// Standalone script to initialize STT provider database
// Usage: go run scripts/init_stt_db.go [db_path]

func main() {
	dbPath := "stt_providers.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	fmt.Printf("Initializing STT provider database at: %s\n\n", dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Create schema
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
		CREATE INDEX IF NOT EXISTS idx_stt_priority ON stt_providers(priority DESC);
	`

	if _, err := db.Exec(schema); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Created stt_providers table")
	fmt.Println("✓ Created indexes")

	// Insert all providers
	providers := []struct {
		name     string
		apiKey   string
		model    string
		language string
		priority int
	}{
		{"deepgram", "", "nova-2", "", 1},
		{"assemblyai", "", "universal", "", 2},
		{"groq", "", "whisper-large-v3", "", 5},
		{"openai", "", "whisper-1", "", 6},
		{"cartesia", "", "ink-whisper", "", 7},
		{"elevenlabs", "", "scribe_v2", "", 8},
		{"azure", "", "latest", "", 9},
		{"google", "", "latest_long", "", 10},
		{"aws", "", "Conversational", "", 11},
		{"soniox", "", "standard_v2", "", 12},
		{"speechmatics", "", "2.0-a", "", 13},
		{"gladia", "", "gladia-2", "", 14},
	}

	for _, p := range providers {
		_, err := db.Exec(`
			INSERT OR IGNORE INTO stt_providers
			(provider_name, api_key, model, language, is_active, priority)
			VALUES (?, ?, ?, ?, 0, ?)
		`, p.name, p.apiKey, p.model, p.language, p.priority)

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inserting %s: %v\n", p.name, err)
		} else {
			fmt.Printf("✓ Registered provider: %-15s (model: %s, priority: %d)\n",
				p.name, p.model, p.priority)
		}
	}

	// Show summary
	fmt.Println("\n--- Summary ---")
	var count int
	db.QueryRow("SELECT COUNT(*) FROM stt_providers").Scan(&count)
	fmt.Printf("Total providers: %d\n", count)

	var activeName string
	err = db.QueryRow("SELECT provider_name FROM stt_providers WHERE is_active = 1 LIMIT 1").Scan(&activeName)
	if err == sql.ErrNoRows {
		fmt.Println("Active provider: None (set with: UPDATE stt_providers SET is_active = 1 WHERE provider_name = 'deepgram')")
	} else if err == nil {
		fmt.Printf("Active provider: %s\n", activeName)
	}

	fmt.Printf("\nDatabase initialized successfully!\n")
	fmt.Printf("\nTo activate a provider:\n")
	fmt.Printf("  UPDATE stt_providers SET is_active = 1 WHERE provider_name = 'deepgram';\n")
	fmt.Printf("  UPDATE stt_providers SET is_active = 0 WHERE provider_name != 'deepgram';\n\n")
	fmt.Printf("To set API key:\n")
	fmt.Printf("  UPDATE stt_providers SET api_key = 'your-key-here' WHERE provider_name = 'deepgram';\n\n")
}
