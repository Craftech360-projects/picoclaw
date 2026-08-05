package openai_compat

import (
	"testing"
)

// The pin must reach the wire, apply only to OpenRouter, and stay off unless
// asked for. Getting any of these wrong is silent: requests keep succeeding,
// they just keep hitting the 52-second upstream this exists to avoid.
func TestOpenRouterProviderPin(t *testing.T) {
	const openRouter = "https://openrouter.ai/api/v1"

	tests := []struct {
		name      string
		apiBase   string
		env       string
		wantOrder []string
	}{
		{
			name:      "pins the configured order on openrouter",
			apiBase:   openRouter,
			env:       "DeepInfra,Crusoe",
			wantOrder: []string{"DeepInfra", "Crusoe"},
		},
		{
			name:      "tolerates spaces and empty entries",
			apiBase:   openRouter,
			env:       " DeepInfra , , Crusoe ,",
			wantOrder: []string{"DeepInfra", "Crusoe"},
		},
		{
			name:    "unset leaves routing to openrouter",
			apiBase: openRouter,
			env:     "",
		},
		{
			// Sending an OpenRouter-only field to another OpenAI-compatible host
			// risks a 400 on a provider that rejects unknown fields.
			name:    "never sent to a non-openrouter host",
			apiBase: "https://api.openai.com/v1",
			env:     "DeepInfra,Crusoe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENROUTER_PROVIDER_ORDER", tt.env)

			p := NewProvider("k", tt.apiBase, "")
			body := p.buildRequestBody(
				[]Message{{Role: "user", Content: "hi"}},
				nil,
				"google/gemma-4-31b-it",
				nil,
			)

			raw, present := body["provider"]
			if tt.wantOrder == nil {
				if present {
					t.Fatalf("provider field should be absent, got %#v", raw)
				}
				return
			}

			if !present {
				t.Fatal("provider field missing from request body")
			}
			pin, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("provider field is %T, want map[string]any", raw)
			}

			order, ok := pin["order"].([]string)
			if !ok {
				t.Fatalf("order is %T, want []string", pin["order"])
			}
			if len(order) != len(tt.wantOrder) {
				t.Fatalf("order = %v, want %v", order, tt.wantOrder)
			}
			for i, want := range tt.wantOrder {
				if order[i] != want {
					t.Errorf("order[%d] = %q, want %q", i, order[i], want)
				}
			}

			// A hard pin turns one bad upstream into a failed greeting; the
			// child should hear a slow answer rather than nothing.
			if fallbacks, ok := pin["allow_fallbacks"].(bool); !ok || !fallbacks {
				t.Errorf("allow_fallbacks = %v, want true", pin["allow_fallbacks"])
			}
		})
	}
}
