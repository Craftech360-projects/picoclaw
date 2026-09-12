package gptlive

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionStartMarshalsLikeThePythonPlugin(t *testing.T) {
	ev := sessionStartEvent{Type: EventSessionStart, EventID: "session_start_1", Session: SessionConfig{
		Model:        DefaultModel,
		Instructions: "You are Cheeko.",
		Input:        []InputItem{TextItem("user", "hi"), TextItem("assistant", "hello")},
		Audio:        &AudioConfig{Format: &AudioFormat{Type: "audio/pcm", Rate: 16000}, Output: &AudioOutput{Voice: "marin"}},
		Delegation:   &Delegation{Type: "responses", Responses: &ResponsesConfig{Model: DefaultBackendModel, Instructions: "Use tools."}},
	}}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"type":"session.start"`,
		`"model":"gpt-live-1"`,
		`"format":{"type":"audio/pcm","rate":16000}`,
		`"output":{"voice":"marin"}`,
		`"delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna","instructions":"Use tools."}}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, `"tools"`) || strings.Contains(got, `"tool_choice"`) {
		t.Errorf("unset optional fields must be omitted: %s", got)
	}
}

func TestContextAppendKeepsNullDelegationID(t *testing.T) {
	raw, _ := json.Marshal(encodeAppend(EventCommentaryAppend, "say hi", nil))
	if !strings.Contains(string(raw), `"delegation_id":null`) {
		t.Errorf("delegation_id must be present even when null: %s", raw)
	}
	id := "d_1"
	raw, _ = json.Marshal(encodeAppend(EventThinkingAppend, "ctx", &id))
	if !strings.Contains(string(raw), `"delegation_id":"d_1"`) || !strings.Contains(string(raw), `"type":"session.thinking.append"`) {
		t.Errorf("unexpected append: %s", raw)
	}
}

func TestServerEventDecodesWithoutValidation(t *testing.T) {
	samples := []string{
		`{"type":"session.started","session":{"id":"live_1"},"unknown":1}`,
		`{"type":"session.output_transcript.delta","delta":"Hi ","start_ms":120,"end_ms":400}`,
		`{"type":"response.event","delegation_id":"d_1","event":{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_time_date","arguments":"{}"}}}`,
		`{"type":"session.usage.updated","usage":{"seconds":31.0},"context_window":{"usage_ratio":0.04}}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"forbidden","message":"Voice session access denied."}}`,
	}
	var evs []ServerEvent
	for _, s := range samples {
		var ev ServerEvent
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		evs = append(evs, ev)
	}
	if evs[0].Session == nil || evs[0].Session.ID != "live_1" {
		t.Errorf("session id not decoded: %+v", evs[0])
	}
	if evs[1].Delta != "Hi " || evs[1].StartMs == nil || *evs[1].StartMs != 120 {
		t.Errorf("transcript delta not decoded: %+v", evs[1])
	}
	if evs[2].Event == nil || evs[2].Event.Item == nil || evs[2].Event.Item.CallID != "call_1" || evs[2].DelegationID == nil {
		t.Errorf("function call not decoded: %+v", evs[2])
	}
	if evs[3].Usage == nil || evs[3].Usage.Seconds != 31 {
		t.Errorf("usage not decoded: %+v", evs[3])
	}
	if evs[4].Error == nil || evs[4].Error.Code != "forbidden" || !evs[4].Error.Fatal() {
		t.Errorf("forbidden must be fatal: %+v", evs[4])
	}
}
