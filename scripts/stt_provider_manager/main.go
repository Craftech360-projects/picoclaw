package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/voice/stt"
)

// CLI tool for managing STT providers
// Usage: go run scripts/stt_provider_manager.go [command] [options]

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	dbPath := "stt_providers.db"
	command := os.Args[1]

	// Find db path from args or use default
	for i, arg := range os.Args {
		if arg == "--db" && i+1 < len(os.Args) {
			dbPath = os.Args[i+1]
			break
		}
	}

	factory, err := stt.NewFactory(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing factory: %v\n", err)
		os.Exit(1)
	}
	defer factory.Close()

	switch command {
	case "list":
		cmdList(factory)
	case "activate":
		if len(os.Args) < 3 {
			fmt.Println("Usage: stt_provider_manager activate <provider-name>")
			os.Exit(1)
		}
		cmdActivate(factory, os.Args[2])
	case "set-key":
		if len(os.Args) < 4 {
			fmt.Println("Usage: stt_provider_manager set-key <provider-name> <api-key>")
			os.Exit(1)
		}
		cmdSetKey(factory, os.Args[2], os.Args[3])
	case "capabilities":
		if len(os.Args) < 3 {
			fmt.Println("Usage: stt_provider_manager capabilities <provider-name>")
			os.Exit(1)
		}
		cmdCapabilities(factory, os.Args[2])
	case "status":
		cmdStatus(factory)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`STT Provider Manager

Usage:
  stt_provider_manager <command> [options]

Commands:
  list                        List all providers with status
  activate <provider>         Activate a provider
  set-key <provider> <key>    Set API key for provider
  capabilities <provider>     Show provider capabilities
  status                      Show active provider status

Options:
  --db <path>                 Path to database (default: stt_providers.db)

Examples:
  ./stt_provider_manager list
  ./stt_provider_manager activate deepgram
  ./stt_provider_manager set-key deepgram sk-your-key
  ./stt_provider_manager capabilities groq
  ./stt_provider_manager status --db ~/.picoclaw/stt_providers.db`)
}

func cmdList(factory *stt.Factory) {
	providers, err := factory.ListProvidersDetailed()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing providers: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("STT Providers:")
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-15s %-20s %-10s %-10s %s\n", "Name", "Model", "Active", "Priority", "Language")
	fmt.Println(strings.Repeat("-", 80))

	for _, p := range providers {
		active := "No"
		if p.IsActive {
			active = "Yes"
		}
		fmt.Printf("%-15s %-20s %-10s %-10d %s\n",
			p.Name, p.Model, active, p.Priority, p.Language)
	}
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("Total: %d providers\n\n", len(providers))
}

func cmdActivate(factory *stt.Factory, name string) {
	if err := factory.ActivateProvider(name); err != nil {
		fmt.Fprintf(os.Stderr, "Error activating provider: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Provider '%s' activated successfully\n", name)
}

func cmdSetKey(factory *stt.Factory, name, key string) {
	if err := factory.SetProviderAPIKey(name, key); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting API key: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ API key set for provider '%s'\n", name)
}

func cmdCapabilities(factory *stt.Factory, name string) {
	caps, err := factory.GetProviderCapabilities(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting capabilities: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Provider: %s\n", name)
	fmt.Printf("Models: %v\n", caps.Models)
	fmt.Printf("Languages: %v\n", caps.Languages)
	fmt.Printf("Streaming: %v\n", caps.SupportsStreaming)
	fmt.Printf("Diarization: %v\n", caps.SupportsDiarization)
	fmt.Printf("Multilingual: %v\n", caps.SupportsMultilingual)
}

func cmdStatus(factory *stt.Factory) {
	provider, err := factory.GetActiveProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "No active provider: %v\n", err)
		fmt.Println("No provider currently active")
		return
	}

	fmt.Printf("Active Provider: %s\n", provider.Name())
	caps := provider.Capabilities()
	fmt.Printf("Models: %v\n", caps.Models)
	fmt.Printf("Streaming: %v\n", caps.SupportsStreaming)
	fmt.Printf("Diarization: %v\n", caps.SupportsDiarization)
}
