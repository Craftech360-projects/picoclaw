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

// blockingTool's Execute blocks until release is closed, so a test can force a tool
// call to still be in flight while Close runs.
type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingTool) Execute(_ context.Context, _ string, _ map[string]any) (string, bool) {
	close(b.started)
	<-b.release
	return "released", false
}

// TestCloseWaitsForInFlightToolBeforeClosingEventsChannel guards against the
// executeCall goroutine outliving the session: if events were closed while the
// goroutine was still between Execute returning and its emit(FunctionResult{...}),
// that emit's select has a send-on-closed-channel case ready, which Go can pick and
// which panics the whole process. Close must not let that race happen, but it also
// must not hang forever on a tool that ignores context cancellation, so this checks
// both ends: Close is still blocked while the tool is running, and it completes
// (well within the session's own timeout) once the tool is released.
func TestCloseWaitsForInFlightToolBeforeClosingEventsChannel(t *testing.T) {
	tool := &blockingTool{started: make(chan struct{}), release: make(chan struct{})}
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_3"}})
			f.send(c, map[string]any{"type": EventDelegationCreated, "delegation": map[string]any{"id": "d_3", "target": "responses"}})
			f.send(c, map[string]any{"type": EventResponseEvent, "delegation_id": "d_3", "event": map[string]any{"type": "response.created"}})
			f.send(c, map[string]any{"type": EventResponseEvent, "delegation_id": "d_3", "event": map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{"id": "fc_3", "type": "function_call", "call_id": "call_3", "name": "slow_tool", "arguments": `{}`}}})
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested"})
		}
	})
	cfg := testConfig(f.url())
	cfg.Tools = tool
	s, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-tool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool call never started")
	}

	closeDone := make(chan struct{})
	go func() {
		s.Close(context.Background())
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("Close returned while the tool call was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	close(tool.release)

	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return after the in-flight tool call finished")
	}

	// Draining to completion, rather than crashing this whole test binary with an
	// unrecoverable send-on-closed-channel panic from the executeCall goroutine that
	// was still in flight when Close began, is the actual assertion here: it proves
	// events was not closed out from under that goroutine. (Whether the FunctionResult
	// it tried to emit actually landed in the channel isn't checked: emit's own select
	// races the send against s.ctx.Done(), which Close has by now cancelled, so once a
	// goroutine reaches emit after cancellation either outcome is a legal, unrelated
	// race — see the send/emit contract from Task 3/4 — and is not what this test
	// guards.)
	drained := make(chan struct{})
	go func() {
		for range s.Events() {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("events channel was never closed after Close returned")
	}
}

func TestZeroCallResponseCompletionLeavesNoDelegationEntry(t *testing.T) {
	s := newTestSession()
	s.out = make(chan []byte, 8)
	s.delegations = map[string]*delegatedResponse{}
	s.callToDelegation = map[string]string{}
	d := "d_4"
	s.onResponseEvent(ServerEvent{DelegationID: &d, Event: &ResponsesEvent{Type: "response.created"}})
	s.onResponseEvent(ServerEvent{DelegationID: &d, Event: &ResponsesEvent{Type: "response.completed"}})

	if _, ok := s.delegations[d]; ok {
		t.Error("a response that completed without any function call must not leave a stub delegation entry")
	}
	select {
	case raw := <-s.out:
		t.Errorf("a response with zero function calls must not be continued, got: %s", raw)
	default:
	}
}
