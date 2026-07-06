package main

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/voice/stt"
)

// managerModeSTTFactory is the live STT factory when the worker runs in
// manager-API mode. Registered at startup, then refreshed on each fresh
// /providers/active fetch so STT tracks manager changes on the same TTL tick
// as LLM and TTS. nil in DB mode.
var managerModeSTTFactory *stt.Factory

// refreshManagerSTTFactory points the live STT factory at the manager's current
// active STT provider. Called from the per-session provider resolution whenever
// a fresh /providers/active payload is fetched. No-op until the factory is
// registered, or for DB-backed factories.
func refreshManagerSTTFactory(active managerActiveProviders) {
	if managerModeSTTFactory == nil {
		return
	}
	managerModeSTTFactory.SetActiveProvider(
		strings.TrimSpace(active.STT.Provider),
		strings.TrimSpace(active.STT.Model),
		strings.TrimSpace(active.STT.Language),
		strings.TrimSpace(active.STT.APIKey),
	)
}

// buildSTTFactoryFromActive builds an STT factory from the manager API's active
// STT provider — sourced from the same /livekit/providers/active payload the
// LLM and TTS use — so the worker needs no direct stt_providers DB connection.
//
// The caller fetches the active providers once at startup and passes them here,
// avoiding a second HTTP round-trip. Returns an error when no active STT
// provider is set, signalling the caller to fall back to stt.NewFactory(dbURL).
func buildSTTFactoryFromActive(active managerActiveProviders) (*stt.Factory, error) {
	name := strings.TrimSpace(active.STT.Provider)
	if name == "" {
		return nil, fmt.Errorf("manager returned no active STT provider")
	}
	return stt.NewFactoryFromProviders([]stt.ProviderInfo{{
		Name:     name,
		Model:    strings.TrimSpace(active.STT.Model),
		Language: strings.TrimSpace(active.STT.Language),
		APIKey:   strings.TrimSpace(active.STT.APIKey),
		IsActive: true,
		Priority: 1,
	}})
}
