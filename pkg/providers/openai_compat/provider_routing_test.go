package openai_compat

import (
	"testing"
)

// Latency sorting must reach the wire and must go only to OpenRouter. Getting
// either wrong is silent: requests keep succeeding, they just go back to being
// routed by price, which is what put a 52-second upstream in front of a child.
func TestOpenRouterProviderRouting(t *testing.T) {
	const openRouter = "https://openrouter.ai/api/v1"

	tests := []struct {
		name     string
		apiBase  string
		wantSort string
	}{
		{
			name:     "sorts by latency on openrouter",
			apiBase:  openRouter,
			wantSort: "latency",
		},
		{
			// Sending an OpenRouter-only field to another OpenAI-compatible host
			// risks a 400 on a provider that rejects unknown fields.
			name:    "never sent to a non-openrouter host",
			apiBase: "https://api.openai.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvider("k", tt.apiBase, "")
			body := p.buildRequestBody(
				[]Message{{Role: "user", Content: "hi"}},
				nil,
				"google/gemma-4-31b-it",
				nil,
			)

			raw, present := body["provider"]
			if tt.wantSort == "" {
				if present {
					t.Fatalf("provider field should be absent, got %#v", raw)
				}
				return
			}

			if !present {
				t.Fatal("provider field missing from request body")
			}
			routing, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("provider field is %T, want map[string]any", raw)
			}
			if got := routing["sort"]; got != tt.wantSort {
				t.Errorf("sort = %v, want %q", got, tt.wantSort)
			}
			// One bad upstream should degrade the greeting, not fail it.
			if fallbacks, ok := routing["allow_fallbacks"].(bool); !ok || !fallbacks {
				t.Errorf("allow_fallbacks = %v, want true", routing["allow_fallbacks"])
			}
			// Parasail served 21.7s and 16.9s first-token turns live. Latency sort
			// alone did not keep it out, so the exclusion must reach the wire —
			// and as a slug, since OpenRouter matches slugs, not display names.
			ignored, _ := routing["ignore"].([]string)
			found := false
			for _, v := range ignored {
				if v == "parasail" {
					found = true
				}
			}
			if !found {
				t.Errorf("ignore = %v, want it to contain %q", routing["ignore"], "parasail")
			}
			// Deprioritises the outliers not named above. Seconds, not milliseconds:
			// a 1000x unit error here fails silently by deprioritising everything.
			cutoff, ok := routing["preferred_max_latency"].(map[string]any)
			if !ok {
				t.Fatalf("preferred_max_latency is %T, want an object", routing["preferred_max_latency"])
			}
			if p90, _ := cutoff["p90"].(int); p90 <= 0 || p90 > 10 {
				t.Errorf("preferred_max_latency p90 = %v, want a plausible value in seconds", cutoff["p90"])
			}
		})
	}
}

// OPENROUTER_PROVIDER_ORDER used to pin a named upstream, and unset meant
// OpenRouter's price-weighted default - so forgetting it silently bought the
// slowest routing available. The variable is gone; this pins that it stays gone
// rather than being quietly reintroduced as a second source of truth.
func TestOpenRouterProviderOrderEnvIsIgnored(t *testing.T) {
	t.Setenv("OPENROUTER_PROVIDER_ORDER", "Crusoe,CoreWeave")

	p := NewProvider("k", "https://openrouter.ai/api/v1", "")
	body := p.buildRequestBody(
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"google/gemma-4-31b-it",
		nil,
	)

	routing, ok := body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider field is %T, want map[string]any", body["provider"])
	}
	if _, hasOrder := routing["order"]; hasOrder {
		t.Errorf("order = %#v, want absent: the env var must no longer route", routing["order"])
	}
	if got := routing["sort"]; got != "latency" {
		t.Errorf("sort = %v, want \"latency\" regardless of the env var", got)
	}
}
