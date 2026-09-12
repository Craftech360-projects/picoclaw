package gptlive

import (
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestFunctionToolsFlattenToResponsesShape(t *testing.T) {
	defs := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "get_time_date",
			Description: "Current date and time.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"timezone": map[string]any{"type": "string"}}, "required": []string{}},
		},
	}}
	got := FunctionTools(defs)
	if len(got) != 1 {
		t.Fatalf("want 1 tool, got %d", len(got))
	}
	raw, _ := json.Marshal(got[0])
	want := `{"description":"Current date and time.","name":"get_time_date","parameters":{"properties":{"timezone":{"type":"string"}},"required":[],"type":"object"},"type":"function"}`
	if string(raw) != want {
		t.Errorf("\n got %s\nwant %s", raw, want)
	}
	if _, nested := got[0]["function"]; nested {
		t.Errorf("responses tools are flat, not nested under function")
	}
}

func TestHostedWebSearch(t *testing.T) {
	raw, _ := json.Marshal(HostedWebSearch())
	if string(raw) != `{"type":"web_search"}` {
		t.Errorf("got %s", raw)
	}
}
