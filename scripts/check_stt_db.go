package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

func main() {
	dbURL := "postgresql://postgres.tsiocygczplmnjpqmutc:seg0QTbvLjPt4E8V@aws-1-ap-south-1.pooler.supabase.com:5432/postgres"

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to ping: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== ALL STT Providers ===")
	rows, err := db.Query("SELECT provider_name, model, is_active, priority, LENGTH(api_key) AS key_len FROM stt_providers ORDER BY priority DESC")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Query failed: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-15s %-20s %-10s %-10s %-10s\n", "PROVIDER", "MODEL", "ACTIVE", "PRIORITY", "KEY_LEN")
	fmt.Println("-------------------------------------------------------------------")
	for rows.Next() {
		var name, model string
		var isActive bool
		var priority, keyLen int
		if err := rows.Scan(&name, &model, &isActive, &priority, &keyLen); err != nil {
			fmt.Fprintf(os.Stderr, "Scan failed: %v\n", err)
			continue
		}
		fmt.Printf("%-15s %-20s %-10v %-10d %-10d\n", name, model, isActive, priority, keyLen)
	}

	fmt.Println("\n=== Active Providers Only ===")
	activeRows, err := db.Query("SELECT provider_name, model, priority FROM stt_providers WHERE is_active IS TRUE ORDER BY priority DESC")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Active query failed: %v\n", err)
		os.Exit(1)
	}
	defer activeRows.Close()

	count := 0
	for activeRows.Next() {
		var name, model string
		var priority int
		activeRows.Scan(&name, &model, &priority)
		fmt.Printf(" -> %s (model=%s, priority=%d)\n", name, model, priority)
		count++
	}
	if count == 0 {
		fmt.Println(" (none)")
	}
}
