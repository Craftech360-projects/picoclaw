// cmd/picoclaw-livekit/realtime_provider_test.go
package main

import "testing"

func TestChooseRealtime(t *testing.T) {
	// A key in the environment must never be used: keys come only from the manager rows.
	t.Setenv("OPENAI_API_KEY", "sk-env")
	t.Setenv("XAI_API_KEY", "x-env")
	t.Setenv("GOOGLE_API_KEY", "g-env")

	active := &managerRealtimeProvider{Provider: "openai-gpt-live", Vendor: "openai", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", Voice: "marin", APIKey: "sk-row"}
	activeNoKey := &managerRealtimeProvider{Provider: "openai-gpt-live", Vendor: "openai", Model: "gpt-live-1", Voice: "marin"}
	rows := []managerRealtimeProvider{
		{ProviderName: "google-gemini-live", Vendor: "google", Model: "gemini-3.1-flash-live-preview", Voice: "Kore", APIKey: "g-row"},
		{ProviderName: "xai-grok-voice", Vendor: "xai", Voice: "eve", APIKey: "x-row"},
		{ProviderName: "xai-no-key", Vendor: "xai", Voice: "eve"},
		{ProviderName: "openai-gpt-live", Vendor: "openai", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", Voice: "cinder", APIKey: "sk-row2", APIBase: "wss://example.test/v1/realtime"},
		{ProviderName: "xai-http-base", Vendor: "xai", Voice: "ara", APIKey: "x-row", APIBase: "https://api.x.ai/v1"},
		{ProviderName: "xai-ws-base", Vendor: "XAI", Voice: "ara", APIKey: " x-row ", APIBase: "WSS://api.x.ai/v1/realtime"},
	}
	noKeyRows := []managerRealtimeProvider{
		{ProviderName: "xai-no-key", Vendor: "xai", Voice: "eve"},
		{ProviderName: "openai-no-key", Vendor: "openai"},
	}

	cases := []struct {
		name      string
		active    *managerRealtimeProvider
		rows      []managerRealtimeProvider
		requested string
		want      realtimeChoice
	}{
		{"active row with its key", active, rows, "",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", Voice: "marin"}},
		{"requested row with its own key", active, rows, "google-gemini-live",
			realtimeChoice{Vendor: "google", APIKey: "g-row", Model: "gemini-3.1-flash-live-preview", Voice: "Kore"}},
		{"requested xai row with its key", active, rows, "xai-grok-voice",
			realtimeChoice{Vendor: "xai", APIKey: "x-row", Voice: "eve"}},
		{"no key for the vendor falls back to the active GPT-Live row", active, rows, "xai-no-key",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", Voice: "marin"}},
		{"no key for the vendor, keyless active, falls back to an openai row with a key", activeNoKey, rows, "xai-no-key",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row2", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", BaseURL: "wss://example.test/v1/realtime", Voice: "cinder"}},
		{"nothing has a key", activeNoKey, noKeyRows, "xai-no-key",
			realtimeChoice{Vendor: "openai"}},
		{"openai active row without a key", activeNoKey, nil, "",
			realtimeChoice{Vendor: "openai", Model: "gpt-live-1", Voice: "marin"}},
		{"unknown requested name uses the active row", active, rows, "nope",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", Voice: "marin"}},
		{"no manager at all", nil, nil, "", realtimeChoice{Vendor: "openai"}},
		{"no active row still uses a keyed openai row from the list", nil, rows, "",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row2", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", BaseURL: "wss://example.test/v1/realtime", Voice: "cinder"}},
		{"keyless active xai row falls back to an inactive openai row with a key",
			&managerRealtimeProvider{Provider: "xai-grok-voice", Vendor: "xai", Voice: "eve"}, rows, "",
			realtimeChoice{Vendor: "openai", APIKey: "sk-row2", Model: "gpt-live-1", BackendModel: "gpt-5.6-luna", BaseURL: "wss://example.test/v1/realtime", Voice: "cinder"}},
		{"keyless active xai row with no rows has no key", &managerRealtimeProvider{Provider: "xai-grok-voice", Vendor: "xai"}, nil, "",
			realtimeChoice{Vendor: "openai"}},
		{"requested name matches case-insensitively after trimming", active, rows, "  Google-Gemini-Live ",
			realtimeChoice{Vendor: "google", APIKey: "g-row", Model: "gemini-3.1-flash-live-preview", Voice: "Kore"}},
		{"non-websocket api_base is ignored", active, rows, "xai-http-base",
			realtimeChoice{Vendor: "xai", APIKey: "x-row", Voice: "ara"}},
		{"websocket api_base is used, case-insensitive", active, rows, "xai-ws-base",
			realtimeChoice{Vendor: "xai", APIKey: "x-row", BaseURL: "WSS://api.x.ai/v1/realtime", Voice: "ara"}},
	}
	for _, c := range cases {
		if got := chooseRealtime(c.active, c.rows, c.requested); got != c.want {
			// fake keys only; safe to print
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestRealtimeRowsNeeded(t *testing.T) {
	cases := []struct {
		name      string
		active    *managerRealtimeProvider
		requested string
		want      bool
	}{
		{"nothing requested, keyed openai active", &managerRealtimeProvider{Vendor: "openai", APIKey: "sk"}, "", false},
		{"nothing requested, keyless openai active", &managerRealtimeProvider{Vendor: "openai"}, "", false},
		// With no active row the list is the only thing chooseRealtime can pick from;
		// without it every session on the box fails (M5).
		{"no active row", nil, "", true},
		{"provider requested", &managerRealtimeProvider{Vendor: "openai", APIKey: "sk"}, "xai-grok-voice", true},
		{"keyless active xai", &managerRealtimeProvider{Vendor: "xai"}, "", true},
		{"keyless active google", &managerRealtimeProvider{Vendor: "google", APIKey: "  "}, "", true},
		{"keyed active google", &managerRealtimeProvider{Vendor: "google", APIKey: "g"}, "", false},
	}
	for _, c := range cases {
		if got := realtimeRowsNeeded(c.active, c.requested); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestChooseRealtimeVoice(t *testing.T) {
	if got := chooseRealtimeVoice("google", "", "kore", "Puck"); got != "Kore" {
		t.Errorf("google voice = %q, want Kore (vendor spelling)", got)
	}
	if got := chooseRealtimeVoice("xai", "marin", "Eve"); got != "eve" {
		t.Errorf("xai voice = %q, want eve (GPT-Live voice skipped)", got)
	}
	if got := chooseRealtimeVoice("openai", "aster"); got != "marin" {
		t.Errorf("openai voice = %q, want the default marin", got)
	}
}

func TestRoomMetadataCarriesTheRealtimeProvider(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Cheeko","gptlive":{"voice":"eve","provider":" xai-grok-voice "}}`)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Metadata.GPTLive == nil || bs.Metadata.GPTLive.Provider != "xai-grok-voice" {
		t.Fatalf("gptlive.provider not parsed: %+v", bs.Metadata.GPTLive)
	}
}
