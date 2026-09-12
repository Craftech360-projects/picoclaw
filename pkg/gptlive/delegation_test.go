package gptlive

import (
	"context"
	"testing"
	"time"
)

type recordingTools struct{ calls []string }

func (r *recordingTools) Execute(_ context.Context, name string, args map[string]any) (string, bool) {
	r.calls = append(r.calls, name)
	if name == "boom" {
		return "kaput", true
	}
	return "Friday 11:18", false
}

func TestDelegatedFunctionCallRunsToolAndContinuesResponse(t *testing.T) {
	var f *fakeLive
	tools := &recordingTools{}
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_1"}})
			f.send(c, map[string]any{"type": EventDelegationCreated, "delegation": map[string]any{"id": "d_1", "target": "responses"}})
			f.send(c, map[string]any{"type": EventResponseEvent, "delegation_id": "d_1", "event": map[string]any{"type": "response.created"}})
			f.send(c, map[string]any{"type": EventResponseEvent, "delegation_id": "d_1", "event": map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "get_time_date", "arguments": `{"timezone":"Asia/Kolkata"}`}}})
			f.send(c, map[string]any{"type": EventResponseEvent, "delegation_id": "d_1", "event": map[string]any{
				"type": "response.completed", "response": map[string]any{"id": "resp_1", "model": "gpt-5.6-luna",
					"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120}}}})
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested"})
		}
	})
	cfg := testConfig(f.url())
	cfg.Tools = tools
	s, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(f.received(EventResponseCreate)) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	s.Close(context.Background())

	if len(tools.calls) != 1 || tools.calls[0] != "get_time_date" {
		t.Fatalf("tool not executed: %v", tools.calls)
	}
	outputs := f.received(EventResponseItemCreate)
	if len(outputs) != 1 {
		t.Fatalf("want one function_call_output, got %d", len(outputs))
	}
	item := outputs[0]["item"].(map[string]any)
	if item["call_id"] != "call_1" || item["output"] != "Friday 11:18" || item["type"] != "function_call_output" {
		t.Errorf("bad output item: %v", item)
	}
	if len(f.received(EventResponseCreate)) != 1 {
		t.Error("the completed response with a returned call must be continued once")
	}
	var usage *BackendUsage
	for ev := range s.Events() {
		if u, ok := ev.(BackendUsage); ok {
			usage = &u
		}
	}
	if usage == nil || usage.Input != 100 || usage.Total != 120 || usage.Model != "gpt-5.6-luna" {
		t.Errorf("backend usage not reported: %+v", usage)
	}
}

func TestToolErrorIsReturnedAsOutputText(t *testing.T) {
	s := newTestSession()
	s.cfg.Tools = &recordingTools{}
	s.out = make(chan []byte, 8)
	s.delegations = map[string]*delegatedResponse{}
	s.callToDelegation = map[string]string{}
	d := "d_2"
	args := `{}`
	s.onResponseEvent(ServerEvent{DelegationID: &d, Event: &ResponsesEvent{Type: "response.created"}})
	s.onResponseEvent(ServerEvent{DelegationID: &d, Event: &ResponsesEvent{Type: "response.output_item.done",
		Item: &OutputItem{ID: "fc", Type: "function_call", CallID: "c1", Name: "boom", Arguments: &args}}})
	select {
	case raw := <-s.out:
		if !contains(raw, `"call_id":"c1"`) || !contains(raw, `"output":"error: kaput"`) {
			t.Errorf("error output not sent: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("no function_call_output sent")
	}
}
