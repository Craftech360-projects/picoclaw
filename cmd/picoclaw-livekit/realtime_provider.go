package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// managerRealtimeProvider is a realtime_providers row. /livekit/providers/active names it
// "provider"; /livekit/providers names it "provider_name".
type managerRealtimeProvider struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`
	Vendor       string `json:"vendor"`
	Model        string `json:"model"`
	BackendModel string `json:"backend_model"`
	Voice        string `json:"voice"`
	APIBase      string `json:"api_base"`
	APIKey       string `json:"api_key"`
}

// realtimeChoice is the vendor, key and model one realtime session runs on.
type realtimeChoice struct {
	Vendor, APIKey, Model, BackendModel, BaseURL, Voice string
}

// realtimeVendors is the set of vendors the realtime pipeline can dial.
var realtimeVendors = map[string]bool{"openai": true, "xai": true, "google": true}

var realtimeVoices = map[string][]string{
	"openai": {"beacon", "cinder", "marin", "stone", "vesper"}, // aster is refused for this OpenAI account
	"xai": {"ara", "eve", "leo", "rex", "sal", "carina", "zagan", "helix", "orion", "luna", "iris", "altair", "zenith",
		"perseus", "helios", "lux", "kepler", "rigel", "cosmo", "celeste", "ursa", "sirius", "lumen", "castor", "naksh", "atlas"},
	"google": {"Puck", "Achernar", "Achird", "Algenib", "Algieba", "Alnilam", "Aoede", "Autonoe", "Callirrhoe", "Charon",
		"Despina", "Enceladus", "Erinome", "Fenrir", "Gacrux", "Iapetus", "Kore", "Laomedeia", "Leda", "Orus", "Pulcherrima",
		"Rasalgethi", "Sadachbia", "Sadaltager", "Schedar", "Sulafat", "Umbriel", "Vindemiatrix", "Zephyr", "Zubenelgenubi"},
}

var realtimeDefaultVoices = map[string]string{"openai": "marin", "xai": "ara", "google": "Puck"}

// realtimeRowVendor normalizes a row's vendor; blank or unknown means openai.
func realtimeRowVendor(r managerRealtimeProvider) string {
	vendor := strings.ToLower(strings.TrimSpace(r.Vendor))
	if !realtimeVendors[vendor] {
		return "openai"
	}
	return vendor
}

// realtimeBaseURL accepts only a websocket api_base; anything else (an https REST base, junk)
// is ignored so the vendor package's default endpoint is used.
func realtimeBaseURL(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://") {
		return base
	}
	return ""
}

func realtimeChoiceFromRow(r managerRealtimeProvider) realtimeChoice {
	return realtimeChoice{Vendor: realtimeRowVendor(r), APIKey: strings.TrimSpace(r.APIKey), Model: strings.TrimSpace(r.Model),
		BackendModel: strings.TrimSpace(r.BackendModel), BaseURL: realtimeBaseURL(r.APIBase), Voice: strings.TrimSpace(r.Voice)}
}

// chooseRealtime picks the row the session asked for (dashboard) or the active row and uses its
// key. API keys come only from the manager rows, never the environment. A Grok or Gemini row with
// no key falls back to GPT-Live on an openai row that has one (the active row first); with none,
// the choice carries an empty key and buildGPTLiveSpec fails the session.
func chooseRealtime(active *managerRealtimeProvider, rows []managerRealtimeProvider, requested string) realtimeChoice {
	row, _ := pickRealtimeRow(active, rows, requested)
	var r managerRealtimeProvider
	if row != nil {
		r = *row
	}
	choice := realtimeChoiceFromRow(r)
	if choice.APIKey != "" || choice.Vendor == "openai" {
		return choice
	}
	if active != nil {
		if c := realtimeChoiceFromRow(*active); c.Vendor == "openai" && c.APIKey != "" {
			return c
		}
	}
	for i := range rows {
		if c := realtimeChoiceFromRow(rows[i]); c.Vendor == "openai" && c.APIKey != "" {
			return c
		}
	}
	return realtimeChoice{Vendor: "openai"}
}

// pickRealtimeRow returns the row whose provider_name matches requested (trimmed, case-insensitive),
// else active. found reports whether a non-blank requested name matched a row.
func pickRealtimeRow(active *managerRealtimeProvider, rows []managerRealtimeProvider, requested string) (row *managerRealtimeProvider, found bool) {
	if requested = strings.TrimSpace(requested); requested != "" {
		for i := range rows {
			if strings.EqualFold(strings.TrimSpace(rows[i].ProviderName), requested) {
				return &rows[i], true
			}
		}
	}
	return active, false
}

// logRealtimeFallback logs, with provider names and vendors only (never keys, URLs or rows), when
// the requested provider was not found or a keyless Grok/Gemini row was replaced by GPT-Live.
func logRealtimeFallback(active *managerRealtimeProvider, rows []managerRealtimeProvider, requested string, choice realtimeChoice) {
	row, found := pickRealtimeRow(active, rows, requested)
	activeName := ""
	if active != nil {
		activeName = active.Provider
	}
	if strings.TrimSpace(requested) != "" && !found {
		logger.WarnCF("livekit", "gptlive: requested realtime provider not found; using the active row", map[string]any{
			"requested_provider": strings.TrimSpace(requested),
			"active_provider":    activeName,
		})
	}
	if row == nil {
		return
	}
	if vendor := realtimeRowVendor(*row); vendor != "openai" && choice.Vendor == "openai" {
		name := row.ProviderName
		if name == "" {
			name = row.Provider
		}
		logger.WarnCF("livekit", "gptlive: realtime provider has no API key; falling back to GPT-Live", map[string]any{
			"provider":        name,
			"vendor":          vendor,
			"fallback_vendor": choice.Vendor,
			"has_key":         choice.APIKey != "",
		})
	}
}

// realtimeRowsNeeded reports whether chooseRealtime needs the full provider list: the dashboard
// named a provider, or the active row is a Grok/Gemini row with no key (GPT-Live fallback lives
// on an inactive openai row).
func realtimeRowsNeeded(active *managerRealtimeProvider, requested string) bool {
	if strings.TrimSpace(requested) != "" {
		return true
	}
	return active != nil && realtimeRowVendor(*active) != "openai" && strings.TrimSpace(active.APIKey) == ""
}

// chooseRealtimeVoice returns the first candidate that is one of the vendor's voices, in the
// vendor's spelling, else the vendor default.
func chooseRealtimeVoice(vendor string, candidates ...string) string {
	for _, c := range candidates {
		for _, v := range realtimeVoices[vendor] {
			if strings.EqualFold(strings.TrimSpace(c), v) {
				return v
			}
		}
	}
	return realtimeDefaultVoices[vendor]
}

func fetchManagerRealtimeProviders(ctx context.Context, cfg config.LiveKitServiceManagerAPIConfig, serviceKey string) ([]managerRealtimeProvider, error) {
	data, err := fetchManagerData(ctx, cfg, serviceKey, "/livekit/providers")
	if err != nil {
		return nil, err
	}
	var out struct {
		Realtime []managerRealtimeProvider `json:"realtime"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode realtime providers: %w", err)
	}
	return out.Realtime, nil
}

// cachedActiveRealtime is the active realtime row from the provider cache that
// resolveLiveKitProviderConfigForSession refreshes at the start of every session.
func cachedActiveRealtime() *managerRealtimeProvider {
	liveKitActiveProvidersCache.mu.Lock()
	defer liveKitActiveProvidersCache.mu.Unlock()
	if !liveKitActiveProvidersCache.hasData {
		return nil
	}
	return liveKitActiveProvidersCache.data.Realtime
}
