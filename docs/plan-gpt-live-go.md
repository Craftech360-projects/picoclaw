# GPT-Live in Go Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run picoclaw's characters on OpenAI GPT-Live from this Go repo: a standalone `pkg/gptlive` protocol package, a second LiveKit pipeline that uses it, and backend function tools that carry the quiz, memory and content logic the cascade did with prompt text.

**Architecture:** `pkg/gptlive` speaks the GPT-Live websocket protocol (session start, PCM in/out, transcripts, delegation, tool calls, reconnect, usage) with no LiveKit imports. `pkg/livekit/gptlive_pipeline.go` bridges a `RoomSession` to it and reuses the worker, room, workspace, quiz bank and persistence code. Character logic becomes tools the backend Responses model calls; the voice model only speaks.

**Tech Stack:** Go 1.25, `github.com/gorilla/websocket` (already a dependency), `github.com/livekit/server-sdk-go/v2` + `media-sdk` (already), `pkg/tools` registry, `pkg/agent` context builder, `pkg/livekit` quiz bank and persistence. No new dependencies. No `openai-go` bump.

**Spec:** `docs/adr/0011-gpt-live-runs-in-go-with-backend-tools.md`

## Global Constraints

- Sample rate is per session: `16000` for device rooms, `24000` for browser rooms. Never resample inside `pkg/gptlive`; the caller feeds PCM16 mono at `Config.SampleRate`.
- `session.started` must arrive before any other client event is sent. `session.close` is sent on shutdown and the client waits up to 5 s for `session.closed`.
- Context appends (`instructions`, `thinking`, `commentary`) are capped at 500 tokens by the service; callers keep them short.
- Startup history is at most 128 items, newest kept.
- Reconnect defaults: 3 retries, 2 s interval, 10 s dial timeout. Fatal error codes (`insufficient_quota`, `invalid_api_key`, `forbidden`) do not retry.
- Noise gate constants: activation 3.0, deactivation 1.8, min silence 0.5 s, window 10 s, floor 1e-4, idle timeout 0.8 s. Transcript speech split: 800 ms gap on the model clock.
- Delegation is `responses` only. `client` delegation is out of scope.
- `pkg/gptlive` imports nothing from `pkg/livekit`, `pkg/agent`, or the LiveKit SDKs. It may import `pkg/providers` for `ToolDefinition` and `pkg/logger`.
- No push-to-talk, no VAD, no `abort` handling in the new pipeline. The data messages `ptt_event`, `speech_end`, `abort` are logged and ignored for GPT-Live sessions.
- Run tests with `go test ./pkg/gptlive/ ./pkg/livekit/ -count=1`. The livekit package needs CGO for TEN VAD on some targets; if `go test ./pkg/livekit/` fails to link locally, use `sh scripts/test-livekit.sh ./pkg/livekit/`.
- Commit after every task with a conventional message: `feat(gptlive): ...`, `feat(livekit): ...`, `refactor(livekit): ...`.

---

## File Structure

**`pkg/gptlive/`** (new, standalone):
- `events.go` — wire types: client events, `ServerEvent`, constants. One responsibility: JSON shapes.
- `schema.go` — picoclaw `providers.ToolDefinition` → Responses function tool maps; hosted web search.
- `session.go` — `Config`, `Session`, `Dial`, goroutines (writer, reader, run loop), `Close`.
- `transcript.go` — per-role speech assembly from transcript deltas; history recording; `Event` types.
- `delegation.go` — backend response events, function-call bookkeeping, tool execution, `response.create` continuation.
- `reconnect.go` — retry loop, history replay, max session duration, state reset.
- `gate.go` — `AdaptiveNoiseGate`.
- `segment.go` — `Segmenter`: bursts (turn boundaries) over the output audio stream.
- `*_test.go` — one per file; `fake_live_test.go` holds the fake server.

**`pkg/livekit/`** (modified):
- `gptlive_pipeline.go` — new: room ↔ session bridge, transcripts and state publishing, greeting, teardown.
- `gptlive_persona.go` — new: voice instructions, backend instructions, greeting from the workspace.
- `gptlive_quiz_tools.go` — new: `QuizTracker` + `quiz_status`, `quiz_score_answer`, `quiz_record_wonder` tools.
- `gptlive_tools.go` — new: `remember_child_fact`, `content_next`, per-character tool set selection.
- `agent_bridge.go` — refactor: extract `doorDirectiveText(q *QuizQuestion, tries int) string` (pure) from `doorDirective`.
- `room_session.go` — modify: `RoomSessionConfig.GPTLive`, branch in `handleTrackSubscribed` and `Join`, ignore PTT/abort for GPT-Live, `Leave` persistence.

**`cmd/picoclaw-livekit/`** (modified):
- `bootstrap_metadata.go` — parse the `gptlive` metadata block.
- `main.go` — `PICOCLAW_LIVEKIT_PIPELINE=gptlive` selects the pipeline; builds persona, tools, quiz tracker, `GPTLive` config.

**`docs/`**:
- `deploy/dev/README.md` — the pm2 app `picoclaw-gptlive` (Task 14).

---

### Task 1: Wire types

**Files:**
- Create: `pkg/gptlive/events.go`
- Test: `pkg/gptlive/events_test.go`

**Interfaces:**
- Produces: `SessionConfig`, `AudioConfig`, `AudioFormat`, `AudioOutput`, `Delegation`, `ResponsesConfig`, `InputItem`, `InputPart`, `TextItem(role, text string) InputItem`, `ServerEvent` and nested types, `FunctionCallOutput`, event type constants, `encodeAppend(kind, content string, delegationID *string) contextAppendEvent`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd D:\picoclaw && go test ./pkg/gptlive/ -run 'TestSessionStart|TestContextAppend|TestServerEvent' -count=1`
Expected: FAIL to compile, "undefined: sessionStartEvent".

- [ ] **Step 3: Write the types**

```go
// Package gptlive speaks OpenAI's GPT-Live full-duplex protocol over a websocket.
//
// Wire shapes mirror LiveKit's Python plugin (gpt_live_types.py): client events are
// serialised with optional fields omitted, server events are decoded loosely so a
// field the service reshapes cannot break a live session.
package gptlive

import "encoding/json"

const (
	DefaultModel        = "gpt-live-1"
	DefaultVoice        = "marin"
	DefaultBackendModel = "gpt-5.6-luna"
	DefaultBaseURL      = "wss://api.openai.com/v1/live/sessions"

	maxInputItems = 128
)

// Client event types.
const (
	EventSessionStart        = "session.start"
	EventSessionUpdate       = "session.update"
	EventInputAudioAppend    = "session.input_audio.append"
	EventInputAudioMute      = "session.input_audio.mute"
	EventInputAudioUnmute    = "session.input_audio.unmute"
	EventInstructionsAppend  = "session.instructions.append"
	EventThinkingAppend      = "session.thinking.append"
	EventCommentaryAppend    = "session.commentary.append"
	EventResponseItemCreate  = "response.item.create"
	EventResponseCreate      = "response.create"
	EventSessionClose        = "session.close"
)

// Server event types.
const (
	EventSessionStarted          = "session.started"
	EventOutputAudioDelta        = "session.output_audio.delta"
	EventInputTranscriptDelta    = "session.input_transcript.delta"
	EventOutputTranscriptDelta   = "session.output_transcript.delta"
	EventDelegationCreated       = "session.delegation.created"
	EventResponseEvent           = "response.event"
	EventSessionUsageUpdated     = "session.usage.updated"
	EventSessionClosed           = "session.closed"
	EventError                   = "error"
)

type AudioFormat struct {
	Type string `json:"type"` // "audio/pcm", "audio/pcmu", "audio/pcma"
	Rate int    `json:"rate"`
}

type AudioOutput struct {
	Voice any `json:"voice,omitempty"` // a name, or map{"id": "voice_..."}
}

type AudioConfig struct {
	Format *AudioFormat `json:"format,omitempty"`
	Output *AudioOutput `json:"output,omitempty"`
}

// ResponsesConfig configures the backend Responses model a delegation is handed to.
type ResponsesConfig struct {
	Model             string           `json:"model,omitempty"`
	Instructions      string           `json:"instructions,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int              `json:"max_output_tokens,omitempty"`
}

type Delegation struct {
	Type      string           `json:"type"` // "responses" | "client"
	Responses *ResponsesConfig `json:"responses,omitempty"`
}

type InputPart struct {
	Type string `json:"type"` // "input_text" | "output_text"
	Text string `json:"text"`
}

type InputItem struct {
	Type    string      `json:"type"` // always "message"
	Role    string      `json:"role"` // "developer" | "user" | "assistant"
	Content []InputPart `json:"content"`
}

// TextItem renders one line of startup history in the shape the service accepts.
func TextItem(role, text string) InputItem {
	part := InputPart{Type: "input_text", Text: text}
	if role == "assistant" {
		part.Type = "output_text"
	}
	return InputItem{Type: "message", Role: role, Content: []InputPart{part}}
}

type SessionConfig struct {
	Model        string       `json:"model"`
	Instructions string       `json:"instructions,omitempty"`
	Input        []InputItem  `json:"input,omitempty"`
	Audio        *AudioConfig `json:"audio,omitempty"`
	Delegation   *Delegation  `json:"delegation,omitempty"`
}

type sessionStartEvent struct {
	Type    string        `json:"type"`
	EventID string        `json:"event_id,omitempty"`
	Session SessionConfig `json:"session"`
}

type sessionUpdateEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
	Session struct {
		Delegation Delegation `json:"delegation"`
	} `json:"session"`
}

type inputAudioAppendEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"` // base64 PCM16 mono at the session rate
}

// simpleEvent covers mute, unmute, response.create and session.close.
type simpleEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
}

// contextAppendEvent keeps delegation_id even when null; the service reads the key.
type contextAppendEvent struct {
	Type         string  `json:"type"`
	EventID      string  `json:"event_id,omitempty"`
	DelegationID *string `json:"delegation_id"`
	Content      string  `json:"content"`
}

func encodeAppend(kind, content string, delegationID *string) contextAppendEvent {
	return contextAppendEvent{Type: kind, EventID: newEventID("append_"), DelegationID: delegationID, Content: content}
}

type FunctionCallOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responseItemCreateEvent struct {
	Type    string             `json:"type"`
	EventID string             `json:"event_id,omitempty"`
	Item    FunctionCallOutput `json:"item"`
}

// ServerEvent is every server event in one loose struct; switch on Type.
type ServerEvent struct {
	Type          string          `json:"type"`
	Session       *SessionInfo    `json:"session,omitempty"`
	Delta         string          `json:"delta,omitempty"`
	StartMs       *int            `json:"start_ms,omitempty"`
	EndMs         *int            `json:"end_ms,omitempty"`
	Delegation    *DelegationInfo `json:"delegation,omitempty"`
	DelegationID  *string         `json:"delegation_id,omitempty"`
	Event         *ResponsesEvent `json:"event,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	ContextWindow *ContextWindow  `json:"context_window,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	Error         *ErrorBody      `json:"error,omitempty"`
}

type SessionInfo struct {
	ID string `json:"id"`
}

type DelegationInfo struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

type ResponsesEvent struct {
	Type     string            `json:"type"`
	Response *ResponseSnapshot `json:"response,omitempty"`
	Item     *OutputItem       `json:"item,omitempty"`
}

type ResponseSnapshot struct {
	ID                string          `json:"id"`
	Model             string          `json:"model"`
	Usage             *ResponseUsage  `json:"usage,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
	IncompleteDetails json.RawMessage `json:"incomplete_details,omitempty"`
}

type ResponseUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

type OutputItem struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	CallID    string  `json:"call_id"`
	Name      string  `json:"name"`
	Arguments *string `json:"arguments"`
}

type Usage struct {
	Seconds float64 `json:"seconds"`
}

type ContextWindow struct {
	UsageRatio *float64 `json:"usage_ratio"`
}

type ErrorBody struct {
	Type          string `json:"type"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	Param         string `json:"param"`
	ClientEventID string `json:"client_event_id"`
}

// Fatal reports whether reconnecting cannot help.
func (e *ErrorBody) Fatal() bool {
	switch e.Code {
	case "insufficient_quota", "invalid_api_key", "forbidden":
		return true
	}
	return false
}
```

Also create `pkg/gptlive/ids.go`:

```go
package gptlive

import (
	"crypto/rand"
	"encoding/hex"
)

func newEventID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/gptlive/events.go pkg/gptlive/ids.go pkg/gptlive/events_test.go
git commit -m "feat(gptlive): wire types for the GPT-Live websocket protocol"
```

---

### Task 2: Tool schema conversion

**Files:**
- Create: `pkg/gptlive/schema.go`
- Test: `pkg/gptlive/schema_test.go`

**Interfaces:**
- Consumes: `providers.ToolDefinition` from `pkg/providers/types.go` (`Type`, `Function{Name, Description, Parameters map[string]any}`).
- Produces: `FunctionTools(defs []providers.ToolDefinition) []map[string]any`, `HostedWebSearch() map[string]any`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/gptlive/ -run 'TestFunctionTools|TestHostedWebSearch' -count=1`
Expected: FAIL, "undefined: FunctionTools".

- [ ] **Step 3: Write the implementation**

```go
package gptlive

import "github.com/sipeed/picoclaw/pkg/providers"

// FunctionTools converts picoclaw tool definitions (chat-completions shape, nested
// under "function") to the flat shape the Responses backend expects.
func FunctionTools(defs []providers.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		if d.Function.Name == "" {
			continue
		}
		params := d.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        d.Function.Name,
			"description": d.Function.Description,
			"parameters":  params,
		})
	}
	return out
}

// HostedWebSearch is OpenAI's server-side web search, run by the backend model.
func HostedWebSearch() map[string]any {
	return map[string]any{"type": "web_search"}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/gptlive/schema.go pkg/gptlive/schema_test.go
git commit -m "feat(gptlive): flatten picoclaw tool definitions to Responses function tools"
```

---

### Task 3: Session dial and handshake

**Files:**
- Create: `pkg/gptlive/session.go`
- Test: `pkg/gptlive/fake_live_test.go`, `pkg/gptlive/session_test.go`

**Interfaces:**
- Produces: `Config`, `ToolExecutor`, `Event` (interface) with `SessionStarted{ID string}`, `Session`, `Dial(ctx, Config) (*Session, error)`, `(*Session).ID() string`, `(*Session).Events() <-chan Event`, `(*Session).Audio() <-chan []byte`, `(*Session).PushAudio([]byte)`, `(*Session).AppendInstructions/AppendThinking/AppendCommentary(string)`, `(*Session).MuteInput()/UnmuteInput()`, `(*Session).Close(ctx) error`, `(*Session).send(v any)`, `(*Session).emit(Event)`, `(*Session).startConfig() SessionConfig`.
- Later tasks add methods on `*Session` in their own files; this task defines the struct fields they use: `cfg`, `out chan []byte`, `events chan Event`, `audio chan []byte`, `started chan struct{}`, `closed chan struct{}`, `historyMu`, `history []InputItem`, `speech map[string]*speech`, `delegations map[string]*delegatedResponse`, `callToDelegation map[string]string`, `sessionID`, `startSent bool`, `closing bool`, `ctx`, `cancel`.

- [ ] **Step 1: Write the fake server**

```go
package gptlive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// fakeLive is a scripted GPT-Live server. handle receives every client event and
// may reply; it runs on the read goroutine, so replies are ordered after the event.
type fakeLive struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	conns   []*websocket.Conn
	handle  func(conn *websocket.Conn, ev map[string]any)
	events  []map[string]any
	upgrade websocket.Upgrader
}

func newFakeLive(t *testing.T, handle func(conn *websocket.Conn, ev map[string]any)) *fakeLive {
	t.Helper()
	f := &fakeLive{t: t, handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		conn, err := f.upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				f.t.Errorf("client sent non-JSON: %s", data)
				continue
			}
			f.mu.Lock()
			f.events = append(f.events, ev)
			f.mu.Unlock()
			f.handle(conn, ev)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLive) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeLive) send(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func (f *fakeLive) received(typ string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, ev := range f.events {
		if ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

// acceptStart is the minimal handler: answer session.start with session.started.
func acceptStart(f *fakeLive) func(conn *websocket.Conn, ev map[string]any) {
	return func(conn *websocket.Conn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			f.send(conn, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_test"}})
		case EventSessionClose:
			f.send(conn, map[string]any{"type": EventSessionClosed, "reason": "close_requested", "usage": map[string]any{"seconds": 3}})
		}
	}
}
```

- [ ] **Step 2: Write the failing session test**

```go
package gptlive

import (
	"context"
	"testing"
	"time"
)

func testConfig(url string) Config {
	return Config{APIKey: "sk-test", BaseURL: url, Model: DefaultModel, Voice: "marin", Instructions: "test",
		SampleRate: 16000, Backend: ResponsesConfig{Model: DefaultBackendModel}, DialTimeout: 2 * time.Second, RetryInterval: 10 * time.Millisecond}
}

func TestDialSendsSessionStartAndWaitsForStarted(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) { acceptStart(f)(c, ev) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if s.ID() != "live_test" {
		t.Errorf("session id %q", s.ID())
	}
	starts := f.received(EventSessionStart)
	if len(starts) != 1 {
		t.Fatalf("want one session.start, got %d", len(starts))
	}
	sess := starts[0]["session"].(map[string]any)
	if sess["instructions"] != "test" || sess["model"] != DefaultModel {
		t.Errorf("session config not sent: %v", sess)
	}
	audio := sess["audio"].(map[string]any)["format"].(map[string]any)
	if audio["rate"].(float64) != 16000 {
		t.Errorf("rate not sent: %v", audio)
	}
	select {
	case ev := <-s.Events():
		if _, ok := ev.(SessionStarted); !ok {
			t.Errorf("first event should be SessionStarted, got %T", ev)
		}
	case <-time.After(time.Second):
		t.Error("no SessionStarted event")
	}
}

func TestNothingIsSentBeforeStarted(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			time.Sleep(150 * time.Millisecond) // the client must hold audio until session.started
			acceptStart(f)(c, ev)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {}()
	s, err := Dial(ctx, testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	s.PushAudio(make([]byte, 640))
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	order := make([]string, 0, len(f.events))
	for _, ev := range f.events {
		order = append(order, ev["type"].(string))
	}
	f.mu.Unlock()
	if len(order) < 2 || order[0] != EventSessionStart || order[1] != EventInputAudioAppend {
		t.Errorf("audio must follow session.start and wait for started; order %v", order)
	}
}

func TestCloseSendsSessionCloseAndWaitsForClosed(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) { acceptStart(f)(c, ev) })
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.received(EventSessionClose)); n != 1 {
		t.Errorf("want one session.close, got %d", n)
	}
	var sawClosed bool
	for ev := range s.Events() {
		if c, ok := ev.(Closed); ok && c.Reason == "close_requested" && c.VoiceSeconds == 3 {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Error("Closed event with final usage not delivered")
	}
}
```

Add `type websocketConn = websocket.Conn` to `fake_live_test.go` (keeps the test signatures short).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/gptlive/ -run 'TestDial|TestNothing|TestClose' -count=1`
Expected: FAIL, "undefined: Dial".

- [ ] **Step 4: Write session.go**

```go
package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// ToolExecutor runs a function call the backend model asked for.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (output string, isError bool)
}

type Config struct {
	APIKey       string
	BaseURL      string // default DefaultBaseURL
	Model        string // default DefaultModel
	Voice        any    // name or map{"id": ...}; default DefaultVoice
	Instructions string // the voice persona; immutable after start
	History      []InputItem
	SampleRate   int // 16000 or 24000
	Backend      ResponsesConfig
	Tools        ToolExecutor // nil: function calls are answered with an error output

	MaxSessionDuration time.Duration // 0: never reconnect on a timer
	MaxRetries         int           // default 3
	RetryInterval      time.Duration // default 2s
	DialTimeout        time.Duration // default 10s
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Voice == nil || c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.SampleRate == 0 {
		c.SampleRate = 24000
	}
	if c.Backend.Model == "" {
		c.Backend.Model = DefaultBackendModel
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = 2 * time.Second
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
	return c
}

// Event is one of the concrete event types below.
type Event interface{ isEvent() }

type SessionStarted struct{ ID string }
type Closed struct {
	Reason       string
	VoiceSeconds float64
}
type Error struct {
	Err         error
	Recoverable bool
}

func (SessionStarted) isEvent() {}
func (Closed) isEvent()         {}
func (Error) isEvent()          {}

const sessionCloseTimeout = 5 * time.Second

type Session struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	out    chan []byte // marshalled client events, written after session.started
	events chan Event
	audio  chan []byte // PCM16 output frames

	mu        sync.Mutex
	sessionID string
	startSent bool
	closing   bool
	started   chan struct{} // closed when session.started arrives
	closedEv  chan struct{} // closed when session.closed arrives

	historyMu sync.Mutex
	history   []InputItem
	speech    map[string]*speech

	delegations      map[string]*delegatedResponse
	callToDelegation map[string]string

	done chan struct{} // closed when the run loop exits
}

// Dial connects, sends session.start and returns once the service answers with
// session.started. The returned session reconnects on its own until Close.
func Dial(ctx context.Context, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	sctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		cfg: cfg, ctx: sctx, cancel: cancel,
		out: make(chan []byte, 512), events: make(chan Event, 256), audio: make(chan []byte, 1024),
		started: make(chan struct{}), closedEv: make(chan struct{}),
		history: append([]InputItem(nil), cfg.History...), speech: map[string]*speech{},
		delegations: map[string]*delegatedResponse{}, callToDelegation: map[string]string{},
		done: make(chan struct{}),
	}
	go s.run()
	select {
	case <-s.started:
		return s, nil
	case <-s.done:
		cancel()
		return nil, errors.New("gptlive: session ended before it started")
	case <-ctx.Done():
		s.cancel()
		return nil, ctx.Err()
	}
}

func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *Session) Events() <-chan Event { return s.events }
func (s *Session) Audio() <-chan []byte { return s.audio }

// PushAudio queues PCM16 mono at Config.SampleRate. Frames are dropped, with a log
// line, only when the outgoing queue is full for over a second.
func (s *Session) PushAudio(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	s.send(inputAudioAppendEvent{Type: EventInputAudioAppend, Audio: base64.StdEncoding.EncodeToString(pcm)})
}

func (s *Session) AppendInstructions(text string) { s.send(encodeAppend(EventInstructionsAppend, text, nil)) }
func (s *Session) AppendThinking(text string)     { s.send(encodeAppend(EventThinkingAppend, text, nil)) }
func (s *Session) AppendCommentary(text string)   { s.send(encodeAppend(EventCommentaryAppend, text, nil)) }
func (s *Session) MuteInput()                     { s.send(simpleEvent{Type: EventInputAudioMute, EventID: newEventID("mute_")}) }
func (s *Session) UnmuteInput()                   { s.send(simpleEvent{Type: EventInputAudioUnmute, EventID: newEventID("unmute_")}) }

func (s *Session) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		logger.WarnCF("gptlive", "marshal client event", map[string]any{"error": err.Error()})
		return
	}
	select {
	case s.out <- data:
	case <-s.ctx.Done():
	case <-time.After(time.Second):
		logger.WarnCF("gptlive", "dropping client event: send queue full", nil)
	}
}

func (s *Session) emit(ev Event) {
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *Session) startConfig() SessionConfig {
	s.historyMu.Lock()
	hist := append([]InputItem(nil), s.history...)
	s.historyMu.Unlock()
	if len(hist) > maxInputItems {
		hist = hist[len(hist)-maxInputItems:]
	}
	backend := s.cfg.Backend
	return SessionConfig{
		Model:        s.cfg.Model,
		Instructions: s.cfg.Instructions,
		Input:        hist,
		Audio:        &AudioConfig{Format: &AudioFormat{Type: "audio/pcm", Rate: s.cfg.SampleRate}, Output: &AudioOutput{Voice: s.cfg.Voice}},
		Delegation:   &Delegation{Type: "responses", Responses: &backend},
	}
}

// Close asks the service to end the session, waits for its final usage, and stops.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		<-s.done
		return nil
	}
	s.closing = true
	startSent := s.startSent
	s.mu.Unlock()
	if startSent {
		s.send(simpleEvent{Type: EventSessionClose, EventID: newEventID("close_")})
		select {
		case <-s.closedEv:
		case <-time.After(sessionCloseTimeout):
		case <-ctx.Done():
		}
	}
	s.cancel()
	<-s.done
	return nil
}

func dialWebsocket(ctx context.Context, cfg Config) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer " + cfg.APIKey}, "User-Agent": {"picoclaw-gptlive"}}
	conn, _, err := websocket.DefaultDialer.DialContext(dctx, cfg.BaseURL, headers)
	if err != nil {
		return nil, fmt.Errorf("gptlive: dial: %w", err)
	}
	return conn, nil
}
```

- [ ] **Step 5: Write the connection loop in the same file**

```go
// runOnce drives one websocket connection until it fails or the session closes.
// It returns nil when the close was ours, an error otherwise.
func (s *Session) runOnce(conn *websocket.Conn) error {
	defer conn.Close()
	start := sessionStartEvent{Type: EventSessionStart, EventID: newEventID("session_start_"), Session: s.startConfig()}
	data, _ := json.Marshal(start)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("gptlive: send session.start: %w", err)
	}
	s.mu.Lock()
	s.startSent = true
	started := s.started
	s.mu.Unlock()

	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			var ev ServerEvent
			if err := json.Unmarshal(msg, &ev); err != nil {
				logger.WarnCF("gptlive", "bad server event", map[string]any{"error": err.Error()})
				continue
			}
			if fatal := s.handleEvent(ev); fatal != nil {
				readErr <- fatal
				return
			}
		}
	}()

	var writeMu sync.Mutex
	write := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, b)
	}
	// hold every client event until session.started, as the protocol requires
	select {
	case <-started:
	case err := <-readErr:
		return s.classify(err)
	case <-s.ctx.Done():
		return nil
	}
	var timer <-chan time.Time
	if s.cfg.MaxSessionDuration > 0 {
		timer = time.After(s.cfg.MaxSessionDuration)
	}
	for {
		select {
		case b := <-s.out:
			if err := write(b); err != nil {
				return fmt.Errorf("gptlive: write: %w", err)
			}
		case err := <-readErr:
			return s.classify(err)
		case <-timer:
			return errReconnectTimer
		case <-s.ctx.Done():
			return nil
		}
	}
}

var errReconnectTimer = errors.New("gptlive: max session duration reached")

// classify turns a read failure into nil when the close was ours.
func (s *Session) classify(err error) error {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	select {
	case <-s.closedEv:
		return nil
	default:
	}
	if closing {
		return nil
	}
	return fmt.Errorf("gptlive: connection closed unexpectedly: %w", err)
}
```

The retry loop `run()` is Task 6; for this task use the minimal version so tests pass:

```go
func (s *Session) run() {
	defer close(s.done)
	defer close(s.events)
	defer close(s.audio)
	conn, err := dialWebsocket(s.ctx, s.cfg)
	if err != nil {
		s.emit(Error{Err: err, Recoverable: false})
		return
	}
	if err := s.runOnce(conn); err != nil {
		s.emit(Error{Err: err, Recoverable: false})
	}
}
```

- [ ] **Step 6: Write the event dispatcher (`handleEvent`) with the handshake and close cases only**

Put this in `session.go`; Tasks 4 and 5 extend the switch.

```go
// handleEvent runs on the read goroutine. It returns a non-nil error only for
// conditions that must end this connection.
func (s *Session) handleEvent(ev ServerEvent) error {
	switch ev.Type {
	case EventSessionStarted:
		s.mu.Lock()
		if ev.Session != nil {
			s.sessionID = ev.Session.ID
		}
		select {
		case <-s.started:
		default:
			close(s.started)
		}
		s.mu.Unlock()
		s.emit(SessionStarted{ID: s.ID()})
	case EventOutputAudioDelta:
		if ev.Delta == "" {
			return nil
		}
		pcm, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil || len(pcm) == 0 {
			return nil
		}
		select {
		case s.audio <- pcm:
		case <-s.ctx.Done():
		}
	case EventInputTranscriptDelta:
		s.onTranscriptDelta("user", ev)
	case EventOutputTranscriptDelta:
		s.onTranscriptDelta("assistant", ev)
	case EventDelegationCreated:
		s.onDelegationCreated(ev)
	case EventResponseEvent:
		s.onResponseEvent(ev)
	case EventSessionUsageUpdated:
		if ev.Usage != nil {
			s.emit(VoiceUsage{Seconds: ev.Usage.Seconds})
		}
	case EventSessionClosed:
		var secs float64
		if ev.Usage != nil {
			secs = ev.Usage.Seconds
		}
		s.emit(Closed{Reason: ev.Reason, VoiceSeconds: secs})
		s.mu.Lock()
		select {
		case <-s.closedEv:
		default:
			close(s.closedEv)
		}
		s.mu.Unlock()
	case EventError:
		if ev.Error == nil {
			return nil
		}
		err := fmt.Errorf("gptlive: %s (%s): %s", ev.Error.Type, ev.Error.Code, ev.Error.Message)
		if ev.Error.Fatal() {
			s.emit(Error{Err: err, Recoverable: false})
			return err
		}
		s.emit(Error{Err: err, Recoverable: true})
	}
	return nil
}
```

Add to the event types: `type VoiceUsage struct{ Seconds float64 }` with `func (VoiceUsage) isEvent() {}`. Add temporary stubs so the package compiles before Tasks 4 and 5: in `transcript.go` `type speech struct{}` and `func (s *Session) onTranscriptDelta(role string, ev ServerEvent) {}`; in `delegation.go` `type delegatedResponse struct{}`, `func (s *Session) onDelegationCreated(ev ServerEvent) {}`, `func (s *Session) onResponseEvent(ev ServerEvent) {}`. Tasks 4 and 5 replace them.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1 -race`
Expected: PASS. If `TestCloseSendsSessionCloseAndWaitsForClosed` hangs, `Close` is not closing `s.events`; the `defer close(s.events)` in `run` must run after `s.cancel()`.

- [ ] **Step 8: Commit**

```bash
git add pkg/gptlive/
git commit -m "feat(gptlive): session dial, handshake, ordered sends and close"
```

---

### Task 4: Transcripts and history

**Files:**
- Create: `pkg/gptlive/transcript.go` (replaces the Task 3 stub)
- Test: `pkg/gptlive/transcript_test.go`

**Interfaces:**
- Produces: events `UserTranscript{ID, Text string; Final bool; StartedAt time.Time}` and `AgentTranscript{ID, Delta, Text string; StartMs, EndMs int}`; `(*Session).History() []InputItem`; `(*Session).endSpeech(role string)`; `(*Session).resetTranscripts()`.
- Rule: a delta whose `start_ms` is more than 800 ms after the previous delta's `end_ms` for the same role starts a new message. A role with no delta for 800 ms of wall clock is also ended (timer), so the last user utterance becomes final without waiting for the next one.

- [ ] **Step 1: Write the failing test**

```go
package gptlive

import (
	"testing"
	"time"
)

func newTestSession() *Session {
	s := &Session{events: make(chan Event, 64), speech: map[string]*speech{}}
	s.ctx, s.cancel = contextWithCancel()
	return s
}

func ms(v int) *int { return &v }

func TestUserTranscriptSplitsOnAnEightHundredMsGap(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "what time", StartMs: ms(0), EndMs: ms(600)})
	s.onTranscriptDelta("user", ServerEvent{Delta: " is it", StartMs: ms(650), EndMs: ms(900)})
	s.onTranscriptDelta("user", ServerEvent{Delta: "tell a story", StartMs: ms(2000), EndMs: ms(2600)}) // 1100 ms gap

	var got []UserTranscript
	for len(s.events) > 0 {
		if ut, ok := (<-s.events).(UserTranscript); ok {
			got = append(got, ut)
		}
	}
	// interim "what time", interim "what time is it", FINAL "what time is it", interim "tell a story"
	if len(got) != 4 {
		t.Fatalf("want 4 user transcript events, got %d: %+v", len(got), got)
	}
	if got[1].Text != "what time is it" || got[1].Final {
		t.Errorf("second delta should extend the same utterance: %+v", got[1])
	}
	if !got[2].Final || got[2].Text != "what time is it" || got[2].ID != got[1].ID {
		t.Errorf("gap must finalise the first utterance: %+v", got[2])
	}
	if got[3].ID == got[2].ID || got[3].Text != "tell a story" {
		t.Errorf("new utterance must get a new id: %+v", got[3])
	}
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != "user" || hist[0].Content[0].Text != "what time is it" {
		t.Errorf("finalised utterance must be in history: %+v", hist)
	}
}

func TestAssistantTranscriptIsRecordedWhenItEnds(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("assistant", ServerEvent{Delta: "Hi ", StartMs: ms(0), EndMs: ms(300)})
	s.onTranscriptDelta("assistant", ServerEvent{Delta: "there", StartMs: ms(320), EndMs: ms(600)})
	s.endSpeech("assistant")
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != "assistant" || hist[0].Content[0].Type != "output_text" || hist[0].Content[0].Text != "Hi there" {
		t.Errorf("assistant speech must land in history as output_text: %+v", hist)
	}
}

func TestIdleTimerFinalisesUserSpeech(t *testing.T) {
	s := newTestSession()
	s.onTranscriptDelta("user", ServerEvent{Delta: "hello", StartMs: ms(0), EndMs: ms(300)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-s.events:
			if ut, ok := ev.(UserTranscript); ok && ut.Final {
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("user speech never finalised by the idle timer")
}
```

Add to `fake_live_test.go`: `func contextWithCancel() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }` with the `context` import.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/gptlive/ -run 'Transcript|IdleTimer' -count=1`
Expected: FAIL, "undefined: UserTranscript" or the stub returns nothing.

- [ ] **Step 3: Write transcript.go**

```go
package gptlive

import (
	"sync"
	"time"
)

const speechGapMs = 800 // a pause this long on the model clock ends a message
const speechIdle = 800 * time.Millisecond

type UserTranscript struct {
	ID        string
	Text      string
	Final     bool
	StartedAt time.Time
}

type AgentTranscript struct {
	ID      string
	Delta   string
	Text    string
	StartMs int
	EndMs   int
}

func (UserTranscript) isEvent()  {}
func (AgentTranscript) isEvent() {}

type speech struct {
	id        string
	text      string
	endMs     *int
	startedAt time.Time
	idle      *time.Timer
}

var speechMu sync.Mutex // guards Session.speech across the read goroutine and idle timers

func (s *Session) onTranscriptDelta(role string, ev ServerEvent) {
	if ev.Delta == "" {
		return
	}
	speechMu.Lock()
	sp := s.speech[role]
	if sp != nil && sp.endMs != nil && ev.StartMs != nil && *ev.StartMs-*sp.endMs > speechGapMs {
		s.endSpeechLocked(role)
		sp = nil
	}
	if sp == nil {
		sp = &speech{id: newEventID("speech_"), startedAt: time.Now()}
		s.speech[role] = sp
	}
	sp.text += ev.Delta
	if ev.EndMs != nil && (sp.endMs == nil || *ev.EndMs > *sp.endMs) {
		v := *ev.EndMs
		sp.endMs = &v
	}
	if sp.idle != nil {
		sp.idle.Stop()
	}
	sp.idle = time.AfterFunc(speechIdle, func() { s.endSpeech(role) })
	id, text, startedAt := sp.id, sp.text, sp.startedAt
	speechMu.Unlock()

	if role == "user" {
		s.emit(UserTranscript{ID: id, Text: text, Final: false, StartedAt: startedAt})
		return
	}
	var start, end int
	if ev.StartMs != nil {
		start = *ev.StartMs
	}
	if ev.EndMs != nil {
		end = *ev.EndMs
	}
	s.emit(AgentTranscript{ID: id, Delta: ev.Delta, Text: text, StartMs: start, EndMs: end})
}

// endSpeech closes a role's open message: the user's becomes final, both go to history.
func (s *Session) endSpeech(role string) {
	speechMu.Lock()
	s.endSpeechLocked(role)
	speechMu.Unlock()
}

func (s *Session) endSpeechLocked(role string) {
	sp := s.speech[role]
	if sp == nil {
		return
	}
	delete(s.speech, role)
	if sp.idle != nil {
		sp.idle.Stop()
	}
	if sp.text == "" {
		return
	}
	s.historyMu.Lock()
	s.history = append(s.history, TextItem(role, sp.text))
	s.historyMu.Unlock()
	if role == "user" {
		s.emit(UserTranscript{ID: sp.id, Text: sp.text, Final: true, StartedAt: sp.startedAt})
	}
}

// History is the conversation so far, as the service accepts it as startup history.
func (s *Session) History() []InputItem {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]InputItem(nil), s.history...)
}

func (s *Session) resetTranscripts() {
	speechMu.Lock()
	defer speechMu.Unlock()
	s.endSpeechLocked("user")
	for role, sp := range s.speech {
		if sp.idle != nil {
			sp.idle.Stop()
		}
		delete(s.speech, role)
	}
}
```

Note `emit` blocks on a full events channel; tests keep the buffer at 64. In production the pipeline drains events continuously.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/gptlive/transcript.go pkg/gptlive/transcript_test.go pkg/gptlive/fake_live_test.go
git commit -m "feat(gptlive): assemble user and agent transcripts into messages and history"
```

---

### Task 5: Delegation and tool calls

**Files:**
- Create: `pkg/gptlive/delegation.go` (replaces the Task 3 stub)
- Test: `pkg/gptlive/delegation_test.go`

**Interfaces:**
- Consumes: `Config.Tools ToolExecutor` (Task 3).
- Produces: events `FunctionCall{CallID, Name, Arguments string}`, `FunctionResult{CallID, Name, Output string; IsError bool}`, `BackendUsage{Model string; Input, Cached, CacheWrite, Output, Reasoning, Total int}`, `DelegationStarted{ID string}`; `(*Session).resetDelegations()`.
- Behaviour, copied from the Python plugin: `response.created` opens a pending response per delegation id; `response.output_item.done` with `type=function_call` records the call, executes it on a goroutine, sends `response.item.create{function_call_output}`; when the response is `completed` and every call has returned, one `response.create` continues it; `failed`/`incomplete` drop the pending response.

- [ ] **Step 1: Write the failing test**

```go
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
```

Add `func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }` to `fake_live_test.go` (import `strings` is already there).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/gptlive/ -run 'Delegated|ToolError' -count=1`
Expected: FAIL, tool never executed / no output sent.

- [ ] **Step 3: Write delegation.go**

```go
package gptlive

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type FunctionCall struct{ CallID, Name, Arguments string }
type FunctionResult struct {
	CallID, Name, Output string
	IsError              bool
}
type BackendUsage struct {
	Model                                          string
	Input, Cached, CacheWrite, Output, Reasoning, Total int
}
type DelegationStarted struct{ ID string }

func (FunctionCall) isEvent()      {}
func (FunctionResult) isEvent()    {}
func (BackendUsage) isEvent()      {}
func (DelegationStarted) isEvent() {}

const toolTimeout = 30 * time.Second

// delegatedResponse follows one backend response: which calls it made and which
// have been answered. The continuation is sent only once both sets match.
type delegatedResponse struct {
	completed bool
	callIDs   map[string]bool
	returned  map[string]bool
}

var delegationMu sync.Mutex

func delegationKey(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func (s *Session) onDelegationCreated(ev ServerEvent) {
	if ev.Delegation == nil || ev.Delegation.ID == "" {
		logger.WarnCF("gptlive", "delegation without id", nil)
		return
	}
	s.emit(DelegationStarted{ID: ev.Delegation.ID})
}

func (s *Session) onResponseEvent(ev ServerEvent) {
	if ev.Event == nil {
		return
	}
	key := delegationKey(ev.DelegationID)
	switch ev.Event.Type {
	case "response.created":
		delegationMu.Lock()
		s.delegations[key] = &delegatedResponse{callIDs: map[string]bool{}, returned: map[string]bool{}}
		delegationMu.Unlock()

	case "response.output_item.done":
		item := ev.Event.Item
		if item == nil || item.Type != "function_call" {
			return
		}
		if item.CallID == "" || item.Name == "" || item.Arguments == nil {
			logger.WarnCF("gptlive", "function call with missing fields", map[string]any{"call_id": item.CallID, "name": item.Name})
			return
		}
		delegationMu.Lock()
		pending := s.delegations[key]
		if pending == nil {
			pending = &delegatedResponse{completed: true, callIDs: map[string]bool{}, returned: map[string]bool{}}
			s.delegations[key] = pending
		}
		pending.callIDs[item.CallID] = true
		s.callToDelegation[item.CallID] = key
		delegationMu.Unlock()
		call := FunctionCall{CallID: item.CallID, Name: item.Name, Arguments: *item.Arguments}
		s.emit(call)
		go s.executeCall(key, call)

	case "response.completed":
		if r := ev.Event.Response; r != nil && r.Usage != nil {
			model := r.Model
			if model == "" {
				model = s.cfg.Backend.Model
			}
			u := r.Usage
			s.emit(BackendUsage{Model: model, Input: u.InputTokens, Cached: u.InputTokensDetails.CachedTokens,
				CacheWrite: u.InputTokensDetails.CacheWriteTokens, Output: u.OutputTokens,
				Reasoning: u.OutputTokensDetails.ReasoningTokens, Total: u.TotalTokens})
		}
		delegationMu.Lock()
		if pending := s.delegations[key]; pending != nil {
			pending.completed = true
		}
		delegationMu.Unlock()
		s.maybeContinue(key)

	case "response.failed", "response.incomplete":
		logger.WarnCF("gptlive", "backend response did not complete", map[string]any{"type": ev.Event.Type, "delegation_id": key})
		delegationMu.Lock()
		if pending := s.delegations[key]; pending != nil {
			for id := range pending.callIDs {
				delete(s.callToDelegation, id)
			}
			delete(s.delegations, key)
		}
		delegationMu.Unlock()
	}
}

func (s *Session) executeCall(key string, call FunctionCall) {
	args := map[string]any{}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		args = map[string]any{}
	}
	output, isErr := "error: no tool executor configured", true
	if s.cfg.Tools != nil {
		ctx, cancel := context.WithTimeout(s.ctx, toolTimeout)
		output, isErr = s.cfg.Tools.Execute(ctx, call.Name, args)
		cancel()
	}
	if isErr {
		output = "error: " + output
	}
	s.emit(FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	s.send(responseItemCreateEvent{Type: EventResponseItemCreate, EventID: newEventID("tool_output_"),
		Item: FunctionCallOutput{Type: "function_call_output", CallID: call.CallID, Output: output}})
	delegationMu.Lock()
	if pending := s.delegations[key]; pending != nil {
		pending.returned[call.CallID] = true
	}
	delegationMu.Unlock()
	s.maybeContinue(key)
}

// maybeContinue sends response.create once the backend has finished asking and
// every call it made has an answer. A partial batch is never continued.
func (s *Session) maybeContinue(key string) {
	delegationMu.Lock()
	pending := s.delegations[key]
	if pending == nil || !pending.completed || len(pending.callIDs) == 0 {
		delegationMu.Unlock()
		return
	}
	for id := range pending.callIDs {
		if !pending.returned[id] {
			delegationMu.Unlock()
			return
		}
	}
	delete(s.delegations, key)
	for id := range pending.callIDs {
		delete(s.callToDelegation, id)
	}
	delegationMu.Unlock()
	s.send(simpleEvent{Type: EventResponseCreate, EventID: newEventID("response_create_")})
}

func (s *Session) resetDelegations() {
	delegationMu.Lock()
	defer delegationMu.Unlock()
	s.delegations = map[string]*delegatedResponse{}
	s.callToDelegation = map[string]string{}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/gptlive/delegation.go pkg/gptlive/delegation_test.go pkg/gptlive/fake_live_test.go
git commit -m "feat(gptlive): run backend function calls and continue the delegated response"
```

---

### Task 6: Reconnect with history replay

**Files:**
- Modify: `pkg/gptlive/session.go` (replace the minimal `run`)
- Create: `pkg/gptlive/reconnect.go`
- Test: `pkg/gptlive/reconnect_test.go`

**Interfaces:**
- Produces: event `Reconnected{}`; `(*Session).resetForReconnect()`.

- [ ] **Step 1: Write the failing test**

```go
package gptlive

import (
	"context"
	"testing"
	"time"
)

func TestReconnectReplaysHistoryAsStartupInput(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		switch ev["type"] {
		case EventSessionStart:
			n := len(f.received(EventSessionStart))
			f.send(c, map[string]any{"type": EventSessionStarted, "session": map[string]any{"id": "live_" + string(rune('0'+n))}})
			if n == 1 {
				f.send(c, map[string]any{"type": EventInputTranscriptDelta, "delta": "hello cheeko", "start_ms": 0, "end_ms": 500})
				time.Sleep(50 * time.Millisecond)
				_ = c.Close() // drop the first connection without session.closed
			}
		case EventSessionClose:
			f.send(c, map[string]any{"type": EventSessionClosed, "reason": "close_requested"})
		}
	})
	s, err := Dial(context.Background(), testConfig(f.url()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.received(EventSessionStart)) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	starts := f.received(EventSessionStart)
	if len(starts) != 2 {
		t.Fatalf("expected a reconnect, got %d session.start", len(starts))
	}
	input, _ := starts[1]["session"].(map[string]any)["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("second start must carry the transcript as history: %v", starts[1])
	}
	msg := input[0].(map[string]any)
	if msg["role"] != "user" || msg["content"].([]any)[0].(map[string]any)["text"] != "hello cheeko" {
		t.Errorf("history item wrong: %v", msg)
	}
	var sawReconnect bool
	timeout := time.After(2 * time.Second)
	for !sawReconnect {
		select {
		case ev := <-s.Events():
			if _, ok := ev.(Reconnected); ok {
				sawReconnect = true
			}
		case <-timeout:
			t.Fatal("no Reconnected event")
		}
	}
}

func TestFatalErrorDoesNotReconnect(t *testing.T) {
	var f *fakeLive
	f = newFakeLive(t, func(c *websocketConn, ev map[string]any) {
		if ev["type"] == EventSessionStart {
			f.send(c, map[string]any{"type": EventError, "error": map[string]any{"type": "invalid_request_error", "code": "forbidden", "message": "Voice session access denied."}})
		}
	})
	_, err := Dial(context.Background(), testConfig(f.url()))
	if err == nil {
		t.Fatal("dial must fail on a fatal error")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.received(EventSessionStart)); n != 1 {
		t.Errorf("fatal error must not retry, got %d starts", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/gptlive/ -run 'Reconnect|FatalError' -count=1`
Expected: FAIL, only one session.start.

- [ ] **Step 3: Write reconnect.go and replace `run`**

```go
package gptlive

import (
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type Reconnected struct{}

func (Reconnected) isEvent() {}

// resetForReconnect: a new connection is a new session, reseeded from history.
// Whatever the dropped one was still carrying never arrives.
func (s *Session) resetForReconnect() {
	s.resetTranscripts()
	s.resetDelegations()
	s.mu.Lock()
	s.sessionID = ""
	s.startSent = false
	s.started = make(chan struct{})
	s.mu.Unlock()
	// drain queued client events; they belonged to the old session
	for {
		select {
		case <-s.out:
		default:
			return
		}
	}
}

func isFatal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "(forbidden)") ||
		strings.Contains(err.Error(), "(invalid_api_key)") || strings.Contains(err.Error(), "(insufficient_quota)")
}

func (s *Session) run() {
	defer close(s.done)
	defer close(s.events)
	defer close(s.audio)
	retries := 0
	reconnecting := false
	for {
		if s.ctx.Err() != nil {
			return
		}
		conn, err := dialWebsocket(s.ctx, s.cfg)
		if err == nil {
			if reconnecting {
				s.resetForReconnect()
				s.emit(Reconnected{})
			}
			err = s.runOnce(conn)
			if err == nil {
				return // our own close, or ctx cancelled
			}
			if errors.Is(err, errReconnectTimer) {
				reconnecting = true
				retries = 0
				continue
			}
		}
		if isFatal(err) || retries >= s.cfg.MaxRetries {
			s.emit(Error{Err: err, Recoverable: false})
			return
		}
		s.emit(Error{Err: err, Recoverable: true})
		logger.WarnCF("gptlive", "connection failed, retrying", map[string]any{"error": err.Error(), "retry": retries + 1})
		retries++
		reconnecting = true
		select {
		case <-time.After(s.cfg.RetryInterval):
		case <-s.ctx.Done():
			return
		}
	}
}
```

Because `resetForReconnect` replaces `s.started`, `runOnce` must read `s.started` under `s.mu` after sending `session.start` (it already does). The `Dial` select also reads `s.started` once; keep it as is, the first connection never resets.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/gptlive/reconnect.go pkg/gptlive/reconnect_test.go pkg/gptlive/session.go
git commit -m "feat(gptlive): reconnect with startup-history replay and fatal-error stop"
```

---

### Task 7: Noise gate and turn segmenter

**Files:**
- Create: `pkg/gptlive/gate.go`, `pkg/gptlive/segment.go`
- Test: `pkg/gptlive/gate_test.go`, `pkg/gptlive/segment_test.go`

**Interfaces:**
- Produces: `NewAdaptiveNoiseGate() *AdaptiveNoiseGate`, `(*AdaptiveNoiseGate).Update(pcm []byte, duration time.Duration) bool`, `(*AdaptiveNoiseGate).Deactivate()`; `NewSegmenter(onOpen, onClose func()) *Segmenter`, `(*Segmenter).Feed(pcm []byte, duration time.Duration)`, `(*Segmenter).Tick(now time.Time)`, `(*Segmenter).Open() bool`.
- The segmenter is the one boundary for agent state: a burst is open exactly while the model is audibly producing output. The pipeline (Task 8) publishes `speaking` on open and `listening` on close.

- [ ] **Step 1: Write the failing tests**

```go
package gptlive

import (
	"math"
	"testing"
	"time"
)

func tone(amplitude float64, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(amplitude * 32767 * math.Sin(float64(i)*2*math.Pi*440/16000))
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	return out
}

const frame = 20 * time.Millisecond // 320 samples at 16 kHz

func TestGateOpensOnSpeechAfterLearningSilence(t *testing.T) {
	g := NewAdaptiveNoiseGate()
	quiet := tone(0.0005, 320)
	for i := 0; i < 50; i++ { // 1 s of near-silence teaches the floor
		if g.Update(quiet, frame) {
			t.Fatalf("gate opened on silence at frame %d", i)
		}
	}
	if !g.Update(tone(0.3, 320), frame) {
		t.Fatal("gate must open on speech well above the floor")
	}
	for i := 0; i < 24; i++ { // 480 ms quiet: still open (min silence 500 ms)
		if !g.Update(quiet, frame) {
			t.Fatalf("gate closed too early at %d ms", (i+1)*20)
		}
	}
	if g.Update(quiet, frame) { // 500 ms reached
		t.Fatal("gate must close after 500 ms below the floor")
	}
}

func TestSegmenterEmitsOpenAndCloseOnce(t *testing.T) {
	var opens, closes int
	seg := NewSegmenter(func() { opens++ }, func() { closes++ })
	quiet := tone(0.0005, 320)
	now := time.Unix(0, 0)
	for i := 0; i < 50; i++ {
		seg.Feed(quiet, frame)
	}
	for i := 0; i < 10; i++ {
		seg.Feed(tone(0.3, 320), frame)
	}
	if opens != 1 || closes != 0 || !seg.Open() {
		t.Fatalf("after speech: opens=%d closes=%d open=%v", opens, closes, seg.Open())
	}
	// the model goes quiet without sending silence frames: the idle timer closes it
	seg.Tick(now.Add(2 * time.Second))
	if opens != 1 || closes != 1 || seg.Open() {
		t.Fatalf("after idle: opens=%d closes=%d open=%v", opens, closes, seg.Open())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/gptlive/ -run 'Gate|Segmenter' -count=1`
Expected: FAIL, "undefined: NewAdaptiveNoiseGate".

- [ ] **Step 3: Write gate.go**

```go
package gptlive

import (
	"encoding/binary"
	"math"
	"time"
)

const (
	gateActivationRatio   = 3.0
	gateDeactivationRatio = 1.8
	gateMinSilence        = 500 * time.Millisecond
	gateWindow            = 10 * time.Second
	gateSilenceFloor      = 1e-4
)

type stretch struct {
	level    float64
	duration time.Duration
}

// AdaptiveNoiseGate opens on output that stands out from the model's own silence.
// The floor is the quietest min-silence stretch the model produced while not
// speaking, within the window; speech never raises it.
type AdaptiveNoiseGate struct {
	history         []stretch
	historyDuration time.Duration
	stretchSum      float64
	stretchDuration time.Duration
	open            bool
	quiet           time.Duration
}

func NewAdaptiveNoiseGate() *AdaptiveNoiseGate { return &AdaptiveNoiseGate{} }

func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

func (g *AdaptiveNoiseGate) Deactivate() {
	g.open = false
	g.quiet = 0
}

// Update reports whether the frame belongs to an open burst of output.
func (g *AdaptiveNoiseGate) Update(pcm []byte, duration time.Duration) bool {
	level := rms(pcm)
	if !g.open {
		g.stretchSum += level * duration.Seconds()
		g.stretchDuration += duration
		if g.stretchDuration >= gateMinSilence {
			mean := g.stretchSum / g.stretchDuration.Seconds()
			g.history = append(g.history, stretch{mean, g.stretchDuration})
			g.historyDuration += g.stretchDuration
			g.stretchSum, g.stretchDuration = 0, 0
			for g.historyDuration > gateWindow && len(g.history) > 1 {
				g.historyDuration -= g.history[0].duration
				g.history = g.history[1:]
			}
		}
	}
	var floor float64
	switch {
	case len(g.history) > 0:
		floor = g.history[0].level
		for _, h := range g.history[1:] {
			if h.level < floor {
				floor = h.level
			}
		}
	case g.stretchDuration > 0:
		floor = g.stretchSum / g.stretchDuration.Seconds()
	default:
		floor = level
	}
	if floor < gateSilenceFloor {
		floor = gateSilenceFloor
	}
	if !g.open {
		if level > floor*gateActivationRatio {
			g.open = true
			g.quiet = 0
		}
	} else if level < floor*gateDeactivationRatio {
		g.quiet += duration
		if g.quiet >= gateMinSilence {
			g.open = false
		}
	} else {
		g.quiet = 0
	}
	return g.open
}
```

- [ ] **Step 4: Write segment.go**

```go
package gptlive

import "time"

const segmentIdle = 800 * time.Millisecond

// Segmenter turns the continuous output stream into bursts: open while the gate
// says the model is audibly speaking, closed on silence or when frames stop coming.
type Segmenter struct {
	gate     *AdaptiveNoiseGate
	open     bool
	lastFeed time.Time
	onOpen   func()
	onClose  func()
	now      func() time.Time
}

func NewSegmenter(onOpen, onClose func()) *Segmenter {
	return &Segmenter{gate: NewAdaptiveNoiseGate(), onOpen: onOpen, onClose: onClose, now: time.Now}
}

func (s *Segmenter) Open() bool { return s.open }

func (s *Segmenter) Feed(pcm []byte, duration time.Duration) {
	s.lastFeed = s.now()
	if s.gate.Update(pcm, duration) {
		if !s.open {
			s.open = true
			s.onOpen()
		}
		return
	}
	if s.open {
		s.close()
	}
}

// Tick closes a burst whose frames stopped arriving; the pipeline calls it on a timer.
func (s *Segmenter) Tick(now time.Time) {
	if s.open && !s.lastFeed.IsZero() && now.Sub(s.lastFeed) >= segmentIdle {
		s.close()
	}
}

func (s *Segmenter) close() {
	s.open = false
	s.gate.Deactivate()
	s.onClose()
}
```

The test's `Tick(now.Add(2*time.Second))` passes a time far after `lastFeed` set by `time.Now()`; make `Feed` use `s.now()` and in the test set `seg.now = func() time.Time { return now }` before feeding, so both clocks agree.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/gptlive/ -count=1 -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/gptlive/gate.go pkg/gptlive/segment.go pkg/gptlive/gate_test.go pkg/gptlive/segment_test.go
git commit -m "feat(gptlive): adaptive noise gate and burst segmenter for turn boundaries"
```

---

## Tools: how character logic reaches the backend model

This section is the design the next three tasks implement. Read it before Task 8.

**Today (cascade).** Quizzy, Bujho, Ginti and Cheeko register zero tools
(`cmd/picoclaw-livekit/workspace_tools.go:384`). Questions reach the LLM through
`{{QUIZ_QUESTIONS}}` in the greeting, the `memory/state/quiz_bank.md` file re-injected
every turn, and a per-turn "Door" system message (`agent_bridge.go:1384`). The verdict is
a `MEMO: type=daily_quiz | date=... | scored_q=... | result=... | answered=...` line the LLM
writes at the end of its reply; `parseQuizVerdict` validates it, `maybePersistQuizState`
writes `memory/state/daily_quiz.md`, and `NewQuizAnswerReporter` posts `/quiz/answer`.

**Under GPT-Live.** The voice model cannot write a MEMO line and cannot receive a per-turn
system message. So:

| Cascade mechanism | GPT-Live replacement |
|---|---|
| `{{QUIZ_QUESTIONS}}` block in greeting + state file | Same rendered block, placed once in the **voice instructions** (so the model can ask without a round trip) and in the **backend instructions** (so it can score). |
| Per-turn Door system message | Returned as the **result text** of `quiz_score_answer`, and pushed to the voice model with `AppendInstructions` by the pipeline after each call. |
| MEMO line parsed from text | `quiz_score_answer` builds the identical MEMO line itself, validates it with `parseQuizVerdict`, persists it with `maybePersistQuizState`, and posts through the existing reporters. Disk and server formats are unchanged. |
| Attempt tracking from `awaiting=` ids | The tool counts misses per question and applies `DoorFor(tries)` and the mastery rule itself. |
| `write_file` guarded to `USER.md` / `memory/MEMORY.md` | `remember_child_fact` appends one dated line to `memory/MEMORY.md`. |
| Wonder question MEMO fields | `quiz_record_wonder(question, answer, code)` → `NewWonderQuestionReporter`. |

**Per-character tool sets** (replaces `liveKitToollessCharacters` for this pipeline):

| Characters | Backend tools |
|---|---|
| everyone | `get_time_date`, `get_weather`, `read_file` (skills), `remember_child_fact`, hosted `web_search` |
| Quizzy, Bujho, Ginti (any persona whose greeting carries a quiz placeholder) | plus `quiz_status`, `quiz_score_answer`, `quiz_record_wonder` |

`quiz_score_answer` semantics (the door ladder lives in the tool, not in the model):

- `result=correct` → terminal. Recorded as `correct`, or as `revealed` when the question
  was at Door 3 (mastery rule, ADR-0009).
- `result=miss` → not terminal. Increments tries, returns the next Door directive. When the
  ladder is exhausted (three tries with an authored ladder, two without) the tool records the
  terminal verdict `revealed` itself and returns the terminal directive.
- `result=revealed` → terminal, for when the child asks for the answer or gives up.

Content characters (jokes, story, words) keep their rendered bank block in both
instruction sets; a `content_next` tool is a later addition once the bank block proves
too long for the voice instructions.

---

### Task 8: Extract the Door directive as a pure function

**Files:**
- Modify: `pkg/livekit/agent_bridge.go:1450-1537` (`doorDirective`)
- Test: `pkg/livekit/quiz_door_text_test.go`

**Interfaces:**
- Produces: `doorDirectiveText(q *QuizQuestion, tries int) string` — identical wording to today's `doorDirective` for the same inputs.

- [ ] **Step 1: Write the failing test**

```go
package livekit

import (
	"strings"
	"testing"
)

func TestDoorDirectiveTextLadder(t *testing.T) {
	q := &QuizQuestion{ID: 7, IDString: "7", ChoiceOrder: []string{"eight", "six"}, TeachText: "four legs each side"}
	if got := doorDirectiveText(q, 0); !strings.Contains(got, "Ask question 7 plainly") {
		t.Errorf("door 1: %q", got)
	}
	if got := doorDirectiveText(q, 1); !strings.Contains(got, `"eight" or "six"`) {
		t.Errorf("door 2: %q", got)
	}
	if got := doorDirectiveText(q, 2); !strings.Contains(got, "four legs each side") || !strings.Contains(got, "Do NOT say the answer") {
		t.Errorf("door 3: %q", got)
	}
	if got := doorDirectiveText(q, 3); !strings.Contains(got, "all three tries") {
		t.Errorf("terminal: %q", got)
	}
	bare := &QuizQuestion{ID: 8, IDString: "8"}
	if got := doorDirectiveText(bare, 0); got != "" {
		t.Errorf("unauthored first ask must be empty, got %q", got)
	}
	if got := doorDirectiveText(bare, 1); !strings.Contains(got, "missed question 8 once") {
		t.Errorf("unauthored second: %q", got)
	}
	if got := doorDirectiveText(bare, 2); !strings.Contains(got, "result=revealed") {
		t.Errorf("unauthored reveal: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/livekit/ -run TestDoorDirectiveTextLadder -count=1`
Expected: FAIL, "undefined: doorDirectiveText".

- [ ] **Step 3: Refactor**

In `agent_bridge.go`, keep everything in `doorDirective` up to and including the `if q == nil { return "" }` check, then replace the remainder of the function body with `return doorDirectiveText(q, tries)`. Move the remainder (from the `// No authored ladder means ...` comment through the final `switch`) verbatim into:

```go
// doorDirectiveText is the Door ladder for one question after `tries` misses. It is
// pure so the GPT-Live quiz tool and the cascade bridge share one wording.
func doorDirectiveText(q *QuizQuestion, tries int) string {
	if q == nil {
		return ""
	}
	// (moved body, unchanged)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/livekit/ -run 'Door' -count=1`
Expected: PASS, including the existing `quiz_door_test.go`.

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/agent_bridge.go pkg/livekit/quiz_door_text_test.go
git commit -m "refactor(livekit): extract the Door directive wording into a pure function"
```

---

### Task 9: Quiz tracker and quiz tools

**Files:**
- Create: `pkg/livekit/gptlive_quiz_tools.go`
- Test: `pkg/livekit/gptlive_quiz_tools_test.go`

**Interfaces:**
- Consumes: `QuizBatch`, `QuizQuestion`, `QuizAttempt`, `doorDirectiveText` (Task 8), `parseQuizVerdict(memo string, batch *QuizBatch, reported map[int64]bool) (int64, string, bool)`, `maybePersistQuizState(workspace, assistantContent string) string`, `doorGuided`, `tools.Tool`, `tools.NewToolResult`, `tools.ErrorResult`.
- Produces: `NewQuizTracker(QuizTrackerConfig) *QuizTracker`, `(*QuizTracker).Status() string`, `(*QuizTracker).Score(questionID, result, transcript string) (directive string, err error)`, `(*QuizTracker).RecordWonder(question, answer, code string)`, `(*QuizTracker).Tools() []tools.Tool`, `(*QuizTracker).OnDirective(func(string))`.

- [ ] **Step 1: Write the failing test**

```go
package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBatch() *QuizBatch {
	return &QuizBatch{Level: 1, Band: "6-8", Bank: "quiz", AnsweredToday: 2, Questions: []QuizQuestion{
		{ID: 11, IDString: "11", Text: "How many legs does a spider have?", Answer: "eight", ChoiceOrder: []string{"eight", "six"}, TeachText: "four legs each side"},
		{ID: 12, IDString: "12", Text: "What colour is the sky on a clear day?", Answer: "blue"},
	}}
}

func TestQuizScoreCorrectPersistsMemoAndReports(t *testing.T) {
	ws := t.TempDir()
	var reported []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: ws, MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { reported = append(reported, result) }})
	directive, err := tr.Score("11", "correct", "eight")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(directive, "Ask question 12 plainly") {
		t.Errorf("after a correct answer the directive must move to the next question: %q", directive)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "memory", "state", "daily_quiz.md"))
	if err != nil {
		t.Fatal(err)
	}
	memo := string(raw)
	for _, want := range []string{"MEMO: type=daily_quiz", "scored_q=11", "result=correct", "answered=3"} {
		if !strings.Contains(memo, want) {
			t.Errorf("memo missing %s: %s", want, memo)
		}
	}
	waitFor(t, func() bool { return len(reported) == 1 && reported[0] == "correct" })
	if _, err := tr.Score("11", "correct", "eight"); err == nil {
		t.Error("a question cannot be scored twice")
	}
}

func TestQuizMissesWalkTheLadderAndRevealAtTheEnd(t *testing.T) {
	var results []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { results = append(results, result) }})
	d1, _ := tr.Score("11", "miss", "six")
	if !strings.Contains(d1, `"eight" or "six"`) {
		t.Errorf("first miss opens Door 2: %q", d1)
	}
	d2, _ := tr.Score("11", "miss", "ten")
	if !strings.Contains(d2, "four legs each side") {
		t.Errorf("second miss opens Door 3: %q", d2)
	}
	d3, _ := tr.Score("11", "miss", "twelve")
	if !strings.Contains(d3, "all three tries") {
		t.Errorf("third miss is terminal: %q", d3)
	}
	waitFor(t, func() bool { return len(results) == 1 })
	if results[0] != "revealed" {
		t.Errorf("exhausted ladder is recorded as revealed, got %s", results[0])
	}
	if got := tr.Status(); !strings.Contains(got, "pending question id=12") {
		t.Errorf("status must move on: %s", got)
	}
}

func TestQuizCorrectAtDoorThreeIsRevealed(t *testing.T) {
	var results []string
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz",
		AnswerReporter: func(id int64, result string, attempts []QuizAttempt) { results = append(results, result) }})
	tr.Score("11", "miss", "six")
	tr.Score("11", "miss", "ten")
	tr.Score("11", "correct", "eight")
	waitFor(t, func() bool { return len(results) == 1 })
	if results[0] != "revealed" {
		t.Errorf("mastery rule: Door 3 correct is revealed, got %s", results[0])
	}
}

func TestQuizToolsExposeTheThreeFunctions(t *testing.T) {
	tr := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir(), MemoType: "daily_quiz"})
	var names []string
	for _, tool := range tr.Tools() {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "quiz_status,quiz_score_answer,quiz_record_wonder" {
		t.Errorf("tools: %v", names)
	}
}
```

Add a helper to the same test file:

```go
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
```
(import `time`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/livekit/ -run 'TestQuiz(Score|Misses|Correct|Tools)' -count=1`
Expected: FAIL, "undefined: NewQuizTracker".

- [ ] **Step 3: Write gptlive_quiz_tools.go**

```go
package livekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/tools"
)

// QuizTrackerConfig wires the tracker to the same bank and reporters the cascade uses.
type QuizTrackerConfig struct {
	Batch           *QuizBatch
	Workspace       string
	MemoType        string // "daily_quiz" | "daily_riddle" | "daily_math"
	AnswerReporter  func(questionID int64, result string, attempts []QuizAttempt)
	AttemptReporter func(questionID int64, attempts []QuizAttempt)
	WonderReporter  func(question, answer, code string)
	Now             func() time.Time
}

// QuizTracker owns the game state a GPT-Live session cannot hold in prose: which
// question is pending, how many tries it has had, and what has been scored.
type QuizTracker struct {
	cfg      QuizTrackerConfig
	mu       sync.Mutex
	reported map[int64]bool
	tries    map[int64]int
	attempts map[int64][]QuizAttempt
	onDirective func(string)
}

func NewQuizTracker(cfg QuizTrackerConfig) *QuizTracker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MemoType == "" {
		cfg.MemoType = "daily_quiz"
	}
	return &QuizTracker{cfg: cfg, reported: map[int64]bool{}, tries: map[int64]int{}, attempts: map[int64][]QuizAttempt{}}
}

// OnDirective registers the pipeline callback that pushes a Door directive to the voice model.
func (t *QuizTracker) OnDirective(fn func(string)) { t.onDirective = fn }

func (t *QuizTracker) find(id string) *QuizQuestion {
	id = strings.TrimSpace(id)
	for i := range t.cfg.Batch.Questions {
		q := &t.cfg.Batch.Questions[i]
		if q.IDString == id || fmt.Sprint(q.ID) == id {
			return q
		}
	}
	return nil
}

func (t *QuizTracker) pendingLocked() *QuizQuestion {
	for i := range t.cfg.Batch.Questions {
		if !t.reported[t.cfg.Batch.Questions[i].ID] {
			return &t.cfg.Batch.Questions[i]
		}
	}
	return nil
}

// Status is what the backend reads when it needs to know where the game is.
func (t *QuizTracker) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	answered := t.cfg.Batch.AnsweredToday + len(t.reported)
	total := t.cfg.Batch.AnsweredToday + len(t.cfg.Batch.Questions)
	q := t.pendingLocked()
	if q == nil {
		return fmt.Sprintf("STATUS: answered=%d of %d today | all questions done", answered, total)
	}
	tries := t.tries[q.ID]
	return fmt.Sprintf("STATUS: answered=%d of %d today | pending question id=%s (door %d, tries %d): %s",
		answered, total, q.IDString, q.DoorFor(tries), tries, q.Text)
}

// Score records one answer. "miss" is not terminal until the ladder is exhausted.
func (t *QuizTracker) Score(questionID, result, transcript string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.find(questionID)
	if q == nil {
		return "", fmt.Errorf("question %q is not in today's batch", questionID)
	}
	if t.reported[q.ID] {
		return "", fmt.Errorf("question %s was already scored", q.IDString)
	}
	transcript = strings.TrimSpace(transcript)
	switch result {
	case "miss":
		t.tries[q.ID]++
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "wrong", Transcript: transcript})
		if !t.ladderExhausted(q) {
			return t.nextDirectiveLocked(q), nil
		}
		return t.recordLocked(q, "revealed")
	case "correct":
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "correct", Transcript: transcript})
		verdict := "correct"
		if q.DoorFor(t.tries[q.ID]) == doorGuided {
			verdict = "revealed" // ADR-0009: a guided answer does not clear
		}
		return t.recordLocked(q, verdict)
	case "revealed":
		t.attempts[q.ID] = append(t.attempts[q.ID], QuizAttempt{Verdict: "revealed", Transcript: transcript})
		return t.recordLocked(q, "revealed")
	}
	return "", errors.New(`result must be "correct", "miss" or "revealed"`)
}

func (t *QuizTracker) ladderExhausted(q *QuizQuestion) bool {
	tries := t.tries[q.ID]
	if len(q.ChoiceOrder) < 2 && strings.TrimSpace(q.TeachText) == "" {
		return tries >= 2 // no authored ladder: the prompt's own two-miss reveal
	}
	return tries >= doorGuided
}

// recordLocked writes the same MEMO line the cascade parses, then reports.
func (t *QuizTracker) recordLocked(q *QuizQuestion, verdict string) (string, error) {
	answered := t.cfg.Batch.AnsweredToday + len(t.reported) + 1
	memo := fmt.Sprintf("MEMO: type=%s | date=%s | scored_q=%s | scored_text=%s | result=%s | answered=%d",
		t.cfg.MemoType, t.cfg.Now().Format("2006-01-02"), q.IDString, strings.ReplaceAll(q.Text, "|", "/"), verdict, answered)
	if _, _, ok := parseQuizVerdict(memo, t.cfg.Batch, t.reported); !ok {
		return "", fmt.Errorf("verdict for question %s did not validate against the bank", q.IDString)
	}
	t.reported[q.ID] = true
	if t.cfg.Workspace != "" {
		maybePersistQuizState(t.cfg.Workspace, memo)
	}
	attempts := append([]QuizAttempt(nil), t.attempts[q.ID]...)
	if t.cfg.AnswerReporter != nil {
		go t.cfg.AnswerReporter(q.ID, verdict, attempts)
	}
	terminal := ""
	if verdict == "revealed" && t.tries[q.ID] > 0 {
		terminal = doorDirectiveText(q, t.tries[q.ID]) // the "all tries used" wording
	}
	next := t.pendingLocked()
	if next == nil {
		return strings.TrimSpace(terminal + "\n\nAll of today's questions are done. Celebrate briefly and move on to free play."), nil
	}
	return strings.TrimSpace(terminal + "\n\n" + t.nextDirectiveLocked(next)), nil
}

func (t *QuizTracker) nextDirectiveLocked(q *QuizQuestion) string {
	d := doorDirectiveText(q, t.tries[q.ID])
	if d == "" {
		d = fmt.Sprintf("## This Question\nAsk question %s plainly, in your own words. Do not offer choices and do not hint yet.", q.IDString)
	}
	if t.onDirective != nil {
		go t.onDirective(d)
	}
	return d
}

func (t *QuizTracker) RecordWonder(question, answer, code string) {
	if t.cfg.WonderReporter != nil {
		go t.cfg.WonderReporter(question, answer, code)
	}
}

// Tools are the three functions the backend model may call.
func (t *QuizTracker) Tools() []tools.Tool {
	return []tools.Tool{quizStatusTool{t}, quizScoreAnswerTool{t}, quizRecordWonderTool{t}}
}

type quizStatusTool struct{ t *QuizTracker }

func (quizStatusTool) Name() string { return "quiz_status" }
func (quizStatusTool) Description() string {
	return "Where today's quiz stands: how many are answered, which question is pending and which Door it is on. Call this before asking a question if unsure."
}
func (quizStatusTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
}
func (q quizStatusTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return tools.NewToolResult(q.t.Status())
}

type quizScoreAnswerTool struct{ t *QuizTracker }

func (quizScoreAnswerTool) Name() string { return "quiz_score_answer" }
func (quizScoreAnswerTool) Description() string {
	return "Score the child's latest answer to a quiz question. Use result=correct when it matches the bank answer, result=miss when it does not (the tool decides hints, choices and when to reveal), result=revealed when the child asked for the answer. Returns the exact instruction for what the voice should do next; follow it."
}
func (quizScoreAnswerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question_id": map[string]any{"type": "string", "description": "The id in parentheses next to the question, e.g. \"11\"."},
			"result":      map[string]any{"type": "string", "enum": []string{"correct", "miss", "revealed"}},
			"transcript":  map[string]any{"type": "string", "description": "What the child said, verbatim."},
		},
		"required": []string{"question_id", "result"},
	}
}
func (q quizScoreAnswerTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	id, _ := args["question_id"].(string)
	result, _ := args["result"].(string)
	transcript, _ := args["transcript"].(string)
	directive, err := q.t.Score(id, result, transcript)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewToolResult(directive)
}

type quizRecordWonderTool struct{ t *QuizTracker }

func (quizRecordWonderTool) Name() string { return "quiz_record_wonder" }
func (quizRecordWonderTool) Description() string {
	return "Record the Wonder Question the child asked today and the answer given, so it can be recalled tomorrow."
}
func (quizRecordWonderTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"answer":   map[string]any{"type": "string"},
			"code":     map[string]any{"type": "string", "description": "The wonder code from the batch, if one was served."},
		},
		"required": []string{"question", "answer"},
	}
}
func (q quizRecordWonderTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	question, _ := args["question"].(string)
	answer, _ := args["answer"].(string)
	code, _ := args["code"].(string)
	q.t.RecordWonder(question, answer, code)
	return tools.SilentResult("recorded")
}
```

If `parseQuizVerdict` rejects the memo because `scored_text` must share content words with the bank question, the memo above uses the bank's own text, so it always matches.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/livekit/ -run 'TestQuiz' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/gptlive_quiz_tools.go pkg/livekit/gptlive_quiz_tools_test.go
git commit -m "feat(livekit): quiz tracker and backend tools that own the Door ladder for GPT-Live"
```

---

### Task 10: Memory tool, per-character tool sets, registry executor

**Files:**
- Create: `pkg/livekit/gptlive_tools.go`
- Test: `pkg/livekit/gptlive_tools_test.go`

**Interfaces:**
- Consumes: `tools.ToolRegistry` (`Register`, `Get`, `ToProviderDefs`, `ExecuteWithContext(ctx, name, args, channel, chatID string, cb tools.AsyncCallback) *tools.ToolResult`), `gptlive.FunctionTools`, `gptlive.HostedWebSearch`.
- Produces: `NewRememberChildFactTool(workspace string) tools.Tool`, `BuildGPTLiveTools(base *tools.ToolRegistry, workspace string, quiz *QuizTracker) *tools.ToolRegistry`, `GPTLiveToolDefs(reg *tools.ToolRegistry, webSearch bool) []map[string]any`, `NewRegistryExecutor(reg *tools.ToolRegistry, sessionKey string) gptlive.ToolExecutor`.
- `gptLiveSharedTools = []string{"get_time_date", "get_weather", "read_file"}` are copied from the base registry when present.

- [ ] **Step 1: Write the failing test**

```go
package livekit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

type echoTool struct{ name string }

func (e echoTool) Name() string        { return e.name }
func (e echoTool) Description() string { return "echo" }
func (e echoTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string"}}, "required": []string{}}
}
func (e echoTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	v, _ := args["v"].(string)
	return tools.NewToolResult(e.name + ":" + v)
}

func TestRememberChildFactAppendsToMemoryFile(t *testing.T) {
	ws := t.TempDir()
	tool := NewRememberChildFactTool(ws)
	res := tool.Execute(context.Background(), map[string]any{"fact": "has a dog named Harry"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	raw, err := os.ReadFile(filepath.Join(ws, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "has a dog named Harry") {
		t.Errorf("fact not written: %s", raw)
	}
	if res := tool.Execute(context.Background(), map[string]any{"fact": ""}); !res.IsError {
		t.Error("empty fact must be rejected")
	}
}

func TestBuildGPTLiveToolsCopiesSharedAndAddsQuiz(t *testing.T) {
	base := tools.NewToolRegistry()
	base.Register(echoTool{"get_time_date"})
	base.Register(echoTool{"exec"}) // never offered
	quiz := NewQuizTracker(QuizTrackerConfig{Batch: testBatch(), Workspace: t.TempDir()})
	reg := BuildGPTLiveTools(base, t.TempDir(), quiz)
	var names []string
	for _, d := range reg.ToProviderDefs() {
		names = append(names, d.Function.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"get_time_date", "remember_child_fact", "quiz_status", "quiz_score_answer", "quiz_record_wonder"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "exec") {
		t.Errorf("exec must never be offered: %s", got)
	}
	defs := GPTLiveToolDefs(reg, true)
	if defs[len(defs)-1]["type"] != "web_search" {
		t.Errorf("hosted web search must be appended last: %v", defs[len(defs)-1])
	}
}

func TestRegistryExecutorReturnsForLLMAndErrorFlag(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(echoTool{"get_time_date"})
	ex := NewRegistryExecutor(reg, "session-1")
	out, isErr := ex.Execute(context.Background(), "get_time_date", map[string]any{"v": "now"})
	if isErr || out != "get_time_date:now" {
		t.Errorf("got %q err=%v", out, isErr)
	}
	if _, isErr := ex.Execute(context.Background(), "nope", nil); !isErr {
		t.Error("unknown tool must be an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/livekit/ -run 'TestRemember|TestBuildGPTLive|TestRegistryExecutor' -count=1`
Expected: FAIL, "undefined: NewRememberChildFactTool".

- [ ] **Step 3: Write gptlive_tools.go**

```go
package livekit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// gptLiveSharedTools are copied from the worker's registry when present. exec,
// write_file, list_dir and web_fetch are deliberately absent.
var gptLiveSharedTools = []string{"get_time_date", "get_weather", "read_file"}

type rememberChildFactTool struct{ workspace string }

func NewRememberChildFactTool(workspace string) tools.Tool { return rememberChildFactTool{workspace} }

func (rememberChildFactTool) Name() string { return "remember_child_fact" }
func (rememberChildFactTool) Description() string {
	return "Remember one durable fact about the child for future sessions: a pet's name, a favourite, a family member. One short sentence per call."
}
func (rememberChildFactTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"fact": map[string]any{"type": "string", "description": "The fact, as one short sentence."}},
		"required":   []string{"fact"},
	}
}
func (r rememberChildFactTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	fact := strings.TrimSpace(strings.ReplaceAll(fmt.Sprint(args["fact"]), "\n", " "))
	if fact == "" || fact == "<nil>" {
		return tools.ErrorResult("fact is required")
	}
	dir := filepath.Join(r.workspace, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tools.ErrorResult(err.Error())
	}
	f, err := os.OpenFile(filepath.Join(dir, "MEMORY.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "- %s: %s\n", time.Now().Format("2006-01-02"), fact); err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.SilentResult("remembered: " + fact)
}

// BuildGPTLiveTools assembles the registry offered to the backend for one session.
func BuildGPTLiveTools(base *tools.ToolRegistry, workspace string, quiz *QuizTracker) *tools.ToolRegistry {
	reg := tools.NewToolRegistry()
	if base != nil {
		for _, name := range gptLiveSharedTools {
			if t, ok := base.Get(name); ok {
				reg.Register(t)
			}
		}
	}
	reg.Register(NewRememberChildFactTool(workspace))
	if quiz != nil {
		for _, t := range quiz.Tools() {
			reg.Register(t)
		}
	}
	return reg
}

// GPTLiveToolDefs renders the registry for the delegation config, hosted search last.
func GPTLiveToolDefs(reg *tools.ToolRegistry, webSearch bool) []map[string]any {
	defs := gptlive.FunctionTools(reg.ToProviderDefs())
	if webSearch {
		defs = append(defs, gptlive.HostedWebSearch())
	}
	return defs
}

type registryExecutor struct {
	reg        *tools.ToolRegistry
	sessionKey string
}

func NewRegistryExecutor(reg *tools.ToolRegistry, sessionKey string) gptlive.ToolExecutor {
	return registryExecutor{reg: reg, sessionKey: sessionKey}
}

func (r registryExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, bool) {
	if _, ok := r.reg.Get(name); !ok {
		return fmt.Sprintf("tool %q is not available", name), true
	}
	res := r.reg.ExecuteWithContext(ctx, name, args, "livekit", r.sessionKey, nil)
	if res == nil {
		return "tool returned nothing", true
	}
	return res.ForLLM, res.IsError
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/livekit/ -run 'TestRemember|TestBuildGPTLive|TestRegistryExecutor|TestQuiz' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/gptlive_tools.go pkg/livekit/gptlive_tools_test.go
git commit -m "feat(livekit): memory tool, per-character GPT-Live tool sets and registry executor"
```

---

### Task 11: Persona: voice instructions, backend instructions, greeting

**Files:**
- Create: `pkg/livekit/gptlive_persona.go`
- Test: `pkg/livekit/gptlive_persona_test.go`

**Interfaces:**
- Consumes: `agent.NewContextBuilder(workspace).BuildSystemPrompt()`, `buildGreetingInstruction(characterName, greetingPrompt string) string`.
- Produces: `GPTLivePersonaInput{Workspace, CharacterName, GreetingPrompt, LanguageName, Accent, BankBlock string; HasQuiz bool}`, `GPTLivePersona{Voice, Backend, Greeting string}`, `BuildGPTLivePersona(in GPTLivePersonaInput) GPTLivePersona`.

- [ ] **Step 1: Write the failing test**

```go
package livekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGPTLivePersonaComposesWorkspaceAndRules(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "AGENT.md"), []byte("You are Quizzy, the quiz master."), 0o644)
	os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("Warm, curious, never sarcastic."), 0o644)
	p := BuildGPTLivePersona(GPTLivePersonaInput{
		Workspace: ws, CharacterName: "Quizzy", GreetingPrompt: "Start with the first question.",
		LanguageName: "Hindi", Accent: "indian", HasQuiz: true,
		BankBlock: "## Today's Quiz Questions (Level 1, band 6-8)\n3. (id=11) How many legs does a spider have? — Answer: eight",
	})
	for _, want := range []string{"You are Quizzy", "Warm, curious", "<delegation>", "Hindi", "<accent>", "(id=11)", "quiz_score_answer"} {
		if !strings.Contains(p.Voice, want) {
			t.Errorf("voice instructions missing %q", want)
		}
	}
	if strings.Contains(p.Voice, "remember_child_fact") {
		t.Error("the voice model must not see tool names")
	}
	for _, want := range []string{"(id=11)", "quiz_score_answer", "child"} {
		if !strings.Contains(p.Backend, want) {
			t.Errorf("backend instructions missing %q", want)
		}
	}
	if !strings.Contains(p.Greeting, "Start with the first question.") || !strings.Contains(p.Greeting, "Quizzy") {
		t.Errorf("greeting: %q", p.Greeting)
	}
	q := BuildGPTLivePersona(GPTLivePersonaInput{Workspace: ws, CharacterName: "Cheeko", Accent: "default"})
	if strings.Contains(q.Voice, "<accent>") || strings.Contains(q.Voice, "quiz_score_answer") {
		t.Error("no accent block and no quiz rules for a plain character")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/livekit/ -run TestBuildGPTLivePersona -count=1`
Expected: FAIL, "undefined: BuildGPTLivePersona".

- [ ] **Step 3: Write gptlive_persona.go**

```go
package livekit

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
)

type GPTLivePersonaInput struct {
	Workspace      string
	CharacterName  string
	GreetingPrompt string
	LanguageName   string // "English" when empty
	Accent         string // "indian" | "default"
	BankBlock      string // RenderQuizQuestions / RenderContentBank output, may be empty
	HasQuiz        bool
}

// GPTLivePersona is everything that is immutable once the session starts.
type GPTLivePersona struct {
	Voice    string // the voice model's instructions
	Backend  string // the backend Responses model's instructions
	Greeting string // commentary sent when the device is ready
}

const gptLiveAccentIndian = `
<accent>
Speak Indian English: an Indian accent with Indian intonation and rhythm, and the everyday
phrasing a child in India hears at home and at school. Keep it natural and warm, never a caricature.
</accent>`

func gptLiveDelegationBlock(language string, hasQuiz bool) string {
	if strings.TrimSpace(language) == "" {
		language = "English"
	}
	var b strings.Builder
	b.WriteString("<delegation>\n")
	b.WriteString("You cannot look things up or keep score yourself. Delegate any question about the current time,\n")
	b.WriteString("date or weather, any factual question you are not sure about, and anything worth remembering\n")
	b.WriteString("about the child. While you wait, say one short cheerful line, then read out the result when it arrives.\n")
	b.WriteString("Answer greetings, small talk, jokes and simple questions yourself.\n")
	if hasQuiz {
		b.WriteString("Every time the child answers a quiz question, delegate so the answer gets scored\n")
		b.WriteString("(the helper calls quiz_score_answer), and then do exactly what the result tells you to do next:\n")
		b.WriteString("ask plainly, offer the two choices, explain then re-ask, or reveal and move on.\n")
		b.WriteString("Never decide on your own whether an answer was right.\n")
	}
	fmt.Fprintf(&b, "Speak %s with the child unless they clearly switch language.\n", language)
	b.WriteString("</delegation>")
	return b.String()
}

func BuildGPTLivePersona(in GPTLivePersonaInput) GPTLivePersona {
	base := agent.NewContextBuilder(in.Workspace).BuildSystemPrompt()
	parts := []string{base, gptLiveDelegationBlock(in.LanguageName, in.HasQuiz)}
	if strings.TrimSpace(in.BankBlock) != "" {
		parts = append(parts, in.BankBlock)
	}
	if in.Accent == "indian" {
		parts = append(parts, strings.TrimSpace(gptLiveAccentIndian))
	}
	backend := "You handle the work a voice model delegates while it talks to a child aged 3 to 16. " +
		"Use tools when current information is required or when the child says something worth remembering. " +
		"Reply with one or two short, friendly, child-safe sentences the voice model can read out."
	if in.HasQuiz {
		backend += " For quiz answers, compare the child's words with the bank answer and accepted answers, " +
			"then call quiz_score_answer with result=correct or result=miss; call quiz_status if unsure which question is pending."
	}
	if strings.TrimSpace(in.BankBlock) != "" {
		backend += "\n\n" + in.BankBlock
	}
	return GPTLivePersona{
		Voice:    strings.Join(parts, "\n\n---\n\n"),
		Backend:  backend,
		Greeting: buildGreetingInstruction(in.CharacterName, in.GreetingPrompt),
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/livekit/ -run TestBuildGPTLivePersona -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/livekit/gptlive_persona.go pkg/livekit/gptlive_persona_test.go
git commit -m "feat(livekit): compose GPT-Live voice and backend instructions from the workspace persona"
```

---

### Task 12: The GPT-Live pipeline in RoomSession

**Files:**
- Create: `pkg/livekit/gptlive_pipeline.go`
- Modify: `pkg/livekit/room_session.go` (`RoomSessionConfig`, `Join` at the `NewAudioPipeline` call near line 736, `handleTrackSubscribed` at line 920, `handleDataMessage` cases at lines 506-608, `Leave` at line 318)
- Test: `pkg/livekit/gptlive_pipeline_test.go`

**Interfaces:**
- Consumes: `gptlive.Dial/Config/Session/Segmenter`, events from Tasks 3-7, `GPTLivePersona` (Task 11), `BuildGPTLiveTools`/`GPTLiveToolDefs`/`NewRegistryExecutor` (Task 10), `QuizTracker.OnDirective` (Task 9), `RoomSession.PublishAgentState`, `rs.room.LocalParticipant.SetAttributes/SendText`, `rs.localTrack.WriteSample`.
- Produces: `GPTLiveSessionSpec{APIKey string; Voice any; SampleRate int; BackendModel string; Persona GPTLivePersona; Tools *tools.ToolRegistry; Quiz *QuizTracker; WebSearch bool; MaxSessionDuration time.Duration}`, `RoomSessionConfig.GPTLive *GPTLiveSessionSpec`, `gptLivePipeline` with `Start(ctx) error`, `WriteSample(media.PCM16Sample) error`, `Greet()`, `Close(ctx)`, `TranscriptSnapshot() []PersistedChatMessage`, `VoiceSeconds() float64`, `BackendTokens() int`.

- [ ] **Step 1: Write the failing test (pure parts only: PCM conversion and transcript bookkeeping)**

```go
package livekit

import (
	"testing"

	"github.com/livekit/media-sdk"
	"github.com/sipeed/picoclaw/pkg/gptlive"
)

func TestPCM16SampleToBytesIsLittleEndian(t *testing.T) {
	got := pcm16ToBytes(media.PCM16Sample{1, -2})
	want := []byte{0x01, 0x00, 0xFE, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got % x want % x", got, want)
	}
	back := bytesToPCM16(got)
	if len(back) != 2 || back[0] != 1 || back[1] != -2 {
		t.Errorf("round trip: %v", back)
	}
}

func TestPipelineRecordsFinalTranscriptsOnly(t *testing.T) {
	p := &gptLivePipeline{}
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hel", Final: false})
	p.onEvent(gptlive.UserTranscript{ID: "u1", Text: "hello", Final: true})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "Hi ", Text: "Hi "})
	p.onEvent(gptlive.AgentTranscript{ID: "a1", Delta: "there", Text: "Hi there"})
	p.onBurstClose()
	snap := p.TranscriptSnapshot()
	if len(snap) != 2 || snap[0].Content != "hello" || snap[0].ChatType != chatTypeUser || snap[1].Content != "Hi there" || snap[1].ChatType != chatTypeAgent {
		t.Errorf("snapshot: %+v", snap)
	}
}
```

Find the `ChatType` values the cascade uses in `TranscriptSnapshot` (`agent_bridge.go:2525`) and define `chatTypeUser`/`chatTypeAgent` constants with those numbers if they are not already named.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/livekit/ -run 'TestPCM16|TestPipelineRecords' -count=1`
Expected: FAIL, "undefined: pcm16ToBytes".

- [ ] **Step 3: Write gptlive_pipeline.go**

```go
package livekit

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"time"

	"github.com/livekit/media-sdk"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type GPTLiveSessionSpec struct {
	APIKey             string
	Voice              any
	SampleRate         int
	BackendModel       string
	Persona            GPTLivePersona
	Tools              *tools.ToolRegistry
	Quiz               *QuizTracker
	WebSearch          bool
	MaxSessionDuration time.Duration
}

const greetingFallbackDelay = 3 * time.Second

type gptLivePipeline struct {
	rs   *RoomSession
	spec GPTLiveSessionSpec
	sess *gptlive.Session
	seg  *gptlive.Segmenter

	mu            sync.Mutex
	state         string
	greeted       bool
	agentText     map[string]string // burst id -> latest full text
	openAgentIDs  []string          // transcript ids seen since the burst opened
	transcript    []PersistedChatMessage
	voiceSeconds  float64
	backendTokens int
	cancel        context.CancelFunc
}

func newGPTLivePipeline(rs *RoomSession, spec GPTLiveSessionSpec) *gptLivePipeline {
	p := &gptLivePipeline{rs: rs, spec: spec, state: "initializing", agentText: map[string]string{}}
	p.seg = gptlive.NewSegmenter(p.onBurstOpen, p.onBurstClose)
	if spec.Quiz != nil {
		spec.Quiz.OnDirective(func(d string) { p.sess.AppendInstructions(d) })
	}
	return p
}

func pcm16ToBytes(s media.PCM16Sample) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func bytesToPCM16(b []byte) media.PCM16Sample {
	out := make(media.PCM16Sample, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// Start dials GPT-Live with the persona and tools and begins the three pumps.
func (p *gptLivePipeline) Start(ctx context.Context) error {
	var defs []map[string]any
	var exec gptlive.ToolExecutor
	if p.spec.Tools != nil {
		defs = GPTLiveToolDefs(p.spec.Tools, p.spec.WebSearch)
		exec = NewRegistryExecutor(p.spec.Tools, p.rs.roomName())
	}
	sess, err := gptlive.Dial(ctx, gptlive.Config{
		APIKey: p.spec.APIKey, Voice: p.spec.Voice, SampleRate: p.spec.SampleRate,
		Instructions: p.spec.Persona.Voice,
		Backend:      gptlive.ResponsesConfig{Model: p.spec.BackendModel, Instructions: p.spec.Persona.Backend, Tools: defs},
		Tools:        exec, MaxSessionDuration: p.spec.MaxSessionDuration,
	})
	if err != nil {
		return err
	}
	p.sess = sess
	pctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.pumpAudioOut(pctx)
	go p.pumpEvents(pctx)
	go p.tickSegmenter(pctx)
	go func() {
		select {
		case <-time.After(greetingFallbackDelay):
			p.Greet()
		case <-pctx.Done():
		}
	}()
	p.setState("listening")
	return nil
}

// WriteSample is the PCMRemoteTrack writer: room audio goes straight to the model.
func (p *gptLivePipeline) WriteSample(sample media.PCM16Sample) error {
	if p.sess != nil {
		p.sess.PushAudio(pcm16ToBytes(sample))
	}
	return nil
}

func (p *gptLivePipeline) Greet() {
	p.mu.Lock()
	if p.greeted || p.sess == nil {
		p.mu.Unlock()
		return
	}
	p.greeted = true
	p.mu.Unlock()
	p.sess.AppendCommentary("Immediately follow the instruction below. Do not wait for the caller to speak first. After that, pause and listen.\n\n" + p.spec.Persona.Greeting)
}

func (p *gptLivePipeline) pumpAudioOut(ctx context.Context) {
	frameDur := func(n int) time.Duration { return time.Duration(n/2) * time.Second / time.Duration(p.spec.SampleRate) }
	for {
		select {
		case pcm, ok := <-p.sess.Audio():
			if !ok {
				return
			}
			p.seg.Feed(pcm, frameDur(len(pcm)))
			if p.rs.localTrack != nil {
				if err := p.rs.localTrack.WriteSample(bytesToPCM16(pcm)); err != nil {
					logger.WarnCF("livekit", "gptlive: write to local track", map[string]any{"error": err.Error()})
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) tickSegmenter(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			p.seg.Tick(now)
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) pumpEvents(ctx context.Context) {
	for {
		select {
		case ev, ok := <-p.sess.Events():
			if !ok {
				return
			}
			p.onEvent(ev)
		case <-ctx.Done():
			return
		}
	}
}

func (p *gptLivePipeline) onEvent(ev gptlive.Event) {
	switch e := ev.(type) {
	case gptlive.UserTranscript:
		p.publishTranscript(e.ID, e.Text, e.Final, true)
		if e.Final {
			p.mu.Lock()
			p.transcript = append(p.transcript, PersistedChatMessage{ChatType: chatTypeUser, Content: e.Text, Timestamp: time.Now().UnixMilli()})
			p.mu.Unlock()
		}
	case gptlive.AgentTranscript:
		p.mu.Lock()
		if _, seen := p.agentText[e.ID]; !seen {
			p.openAgentIDs = append(p.openAgentIDs, e.ID)
		}
		p.agentText[e.ID] = e.Text
		p.mu.Unlock()
		p.publishTranscript(e.ID, e.Text, false, false)
	case gptlive.DelegationStarted:
		p.setState("thinking")
	case gptlive.FunctionCall:
		logger.InfoCF("livekit", "gptlive: tool call", map[string]any{"name": e.Name, "room": p.rs.roomName()})
	case gptlive.FunctionResult:
		if e.IsError {
			logger.WarnCF("livekit", "gptlive: tool error", map[string]any{"name": e.Name, "output": e.Output})
		}
	case gptlive.VoiceUsage:
		p.mu.Lock()
		p.voiceSeconds += e.Seconds
		p.mu.Unlock()
	case gptlive.BackendUsage:
		p.mu.Lock()
		p.backendTokens += e.Total
		p.mu.Unlock()
	case gptlive.Closed:
		p.mu.Lock()
		p.voiceSeconds += e.VoiceSeconds
		p.mu.Unlock()
	case gptlive.Error:
		logger.WarnCF("livekit", "gptlive: session error", map[string]any{"error": e.Err.Error(), "recoverable": e.Recoverable})
	}
}

func (p *gptLivePipeline) onBurstOpen() { p.setState("speaking") }

// onBurstClose finalises the agent turn: publish the final transcript and record it.
func (p *gptLivePipeline) onBurstClose() {
	p.mu.Lock()
	var parts []string
	for _, id := range p.openAgentIDs {
		if t := strings.TrimSpace(p.agentText[id]); t != "" {
			parts = append(parts, t)
		}
		delete(p.agentText, id)
	}
	ids := p.openAgentIDs
	p.openAgentIDs = nil
	text := strings.Join(parts, " ")
	if text != "" {
		p.transcript = append(p.transcript, PersistedChatMessage{ChatType: chatTypeAgent, Content: text, Timestamp: time.Now().UnixMilli()})
	}
	p.mu.Unlock()
	if len(ids) > 0 && text != "" {
		p.publishTranscript(ids[len(ids)-1], text, true, false)
	}
	p.setState("listening")
}

func (p *gptLivePipeline) setState(next string) {
	p.mu.Lock()
	prev := p.state
	p.state = next
	p.mu.Unlock()
	if prev == next || p.rs == nil || p.rs.room == nil {
		return
	}
	p.rs.room.LocalParticipant.SetAttributes(map[string]string{"lk.agent.state": next})
	_ = p.rs.PublishAgentState(prev, next)
}

func (p *gptLivePipeline) publishTranscript(segmentID, text string, final, user bool) {
	if p.rs == nil || p.rs.room == nil || text == "" {
		return
	}
	attrs := map[string]string{"lk.segment_id": segmentID, "lk.final": map[bool]string{true: "true", false: "false"}[final]}
	if user {
		attrs["lk.transcribed_track_id"] = p.rs.remoteAudioTrackSID()
	}
	_ = p.rs.room.LocalParticipant.SendText(text, lksdk.StreamTextOptions{Topic: "lk.transcription", Attributes: attrs})
}

func (p *gptLivePipeline) TranscriptSnapshot() []PersistedChatMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PersistedChatMessage(nil), p.transcript...)
}

func (p *gptLivePipeline) VoiceSeconds() float64 { p.mu.Lock(); defer p.mu.Unlock(); return p.voiceSeconds }
func (p *gptLivePipeline) BackendTokens() int    { p.mu.Lock(); defer p.mu.Unlock(); return p.backendTokens }

func (p *gptLivePipeline) Close(ctx context.Context) {
	if p.sess != nil {
		_ = p.sess.Close(ctx)
	}
	if p.cancel != nil {
		p.cancel()
	}
}
```

Add `func (rs *RoomSession) remoteAudioTrackSID() string` returning the SID of the first subscribed remote audio track (store it in `handleTrackSubscribed` from `track.ID()`), and the two chat-type constants.

- [ ] **Step 4: Wire RoomSession**

1. `RoomSessionConfig`: add `GPTLive *GPTLiveSessionSpec`.
2. `RoomSession`: add field `gptlive *gptLivePipeline`.
3. In `Join`, right after the local track is published and before the existing `NewAudioPipeline` call at line ~736: `if rs.cfg.GPTLive != nil { rs.gptlive = newGPTLivePipeline(rs, *rs.cfg.GPTLive); if err := rs.gptlive.Start(ctx); err != nil { return err } }` and skip the cascade pipeline creation when `rs.gptlive != nil` (the `SampleRate` for the local track comes from `cfg.SampleRate`, which the worker sets to the GPT-Live rate).
4. In `handleTrackSubscribed`, before the STT stream is opened: `if rs.gptlive != nil { pcmTrack, err := lkmedia.NewPCMRemoteTrack(track, rs.gptlive, lkmedia.WithTargetSampleRate(rs.cfg.GPTLive.SampleRate), lkmedia.WithTargetChannels(1)); ...store on ps; rs.setRemoteAudioTrackSID(track.ID()); return }`.
5. In `handleDataMessage`: `case "ready_for_greeting"` → `if rs.gptlive != nil { rs.gptlive.Greet() }` before the existing logging; `case "ptt_event", "speech_end", "abort"` → `if rs.gptlive != nil { logger.DebugCF(...ignored for gptlive...); return }` at the top of each case.
6. In `Leave`, before `rs.persistPostSessionData(bridge)`: `if rs.gptlive != nil { rs.persistGPTLiveSession(); rs.gptlive.Close(ctx); }` (Task 13 adds `persistGPTLiveSession`; for this task add an empty method so it compiles).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./... && go test ./pkg/livekit/ -run 'TestPCM16|TestPipelineRecords' -count=1`
Expected: PASS and the whole repo builds.

- [ ] **Step 6: Commit**

```bash
git add pkg/livekit/gptlive_pipeline.go pkg/livekit/gptlive_pipeline_test.go pkg/livekit/room_session.go
git commit -m "feat(livekit): GPT-Live pipeline: room audio in, model audio out, state and transcripts"
```

---

### Task 13: Worker wiring, dispatch metadata, persistence

**Files:**
- Modify: `cmd/picoclaw-livekit/bootstrap_metadata.go` (struct near line 20-50; parse near line 151)
- Create: `cmd/picoclaw-livekit/gptlive_spec.go`
- Modify: `cmd/picoclaw-livekit/main.go` (the `bridgeFactory` closure that returns `livekit.NewRoomSession` near line 1294)
- Modify: `pkg/livekit/room_session.go` (`persistGPTLiveSession`)
- Test: `cmd/picoclaw-livekit/gptlive_spec_test.go`

**Interfaces:**
- Produces: `roomMetadataGPTLive{Voice, Accent string; Rate int}` on the bootstrap metadata as `GPTLive *roomMetadataGPTLive \`json:"gptlive"\``; `gptLiveMemoTypeFor(character string) string`; `buildGPTLiveSpec(in gptLiveSpecInput) (*livekit.GPTLiveSessionSpec, error)`; `(*RoomSession).persistGPTLiveSession()`.

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestGPTLiveMemoTypeFollowsTheCharacter(t *testing.T) {
	cases := map[string]string{"Quizzy": "daily_quiz", "bujho": "daily_riddle", "Ginti": "daily_math", "Cheeko": ""}
	for in, want := range cases {
		if got := gptLiveMemoTypeFor(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestGPTLiveMetadataDefaults(t *testing.T) {
	bs, err := parseRoomMetadataBootstrap(`{"character":"Cheeko","gptlive":{"voice":"vesper","accent":"indian"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Metadata.GPTLive == nil || bs.Metadata.GPTLive.Voice != "vesper" || bs.Metadata.GPTLive.Accent != "indian" {
		t.Fatalf("gptlive block not parsed: %+v", bs.Metadata.GPTLive)
	}
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{APIKey: "sk", Metadata: bs.Metadata, Workspace: t.TempDir(), CharacterName: "Cheeko"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Voice != "vesper" || spec.SampleRate != 16000 || spec.BackendModel == "" || spec.Quiz != nil {
		t.Errorf("spec defaults wrong: %+v", spec)
	}
	if _, err := buildGPTLiveSpec(gptLiveSpecInput{Metadata: bs.Metadata, Workspace: t.TempDir()}); err == nil {
		t.Error("missing OPENAI_API_KEY must be an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/picoclaw-livekit/ -run 'TestGPTLive' -count=1`
Expected: FAIL, "undefined: gptLiveMemoTypeFor".

- [ ] **Step 3: Write gptlive_spec.go and the metadata field**

In `bootstrap_metadata.go` add to the metadata struct:

```go
	GPTLive *roomMetadataGPTLive `json:"gptlive"`
```

```go
type roomMetadataGPTLive struct {
	Voice  string `json:"voice"`
	Accent string `json:"accent"`
	Rate   int    `json:"rate"`
}
```

and in the parse function, next to the other fields: `if raw, ok := payload["gptlive"].(map[string]any); ok { g := &roomMetadataGPTLive{}; g.Voice = normalizeString(raw["voice"]); g.Accent = normalizeString(raw["accent"]); if r, ok := raw["rate"].(float64); ok { g.Rate = int(r) }; metadata.GPTLive = g }`.

`gptlive_spec.go`:

```go
package main

import (
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/livekit"
	"github.com/sipeed/picoclaw/pkg/tools"
)

var gptLiveVoices = map[string]bool{"aster": true, "beacon": true, "cinder": true, "marin": true, "stone": true, "vesper": true}

func gptLiveMemoTypeFor(character string) string {
	switch strings.ToLower(strings.TrimSpace(character)) {
	case "quizzy":
		return "daily_quiz"
	case "bujho":
		return "daily_riddle"
	case "ginti":
		return "daily_math"
	}
	return ""
}

type gptLiveSpecInput struct {
	APIKey         string
	Metadata       roomMetadata
	Workspace      string
	CharacterName  string
	GreetingPrompt string
	LanguageName   string
	BaseTools      *tools.ToolRegistry
	QuizBatch      *livekit.QuizBatch // nil when the persona has no quiz placeholder
	QuizReporters  livekit.QuizTrackerConfig // AnswerReporter, AttemptReporter, WonderReporter
	BankBlock      string
}

func buildGPTLiveSpec(in gptLiveSpecInput) (*livekit.GPTLiveSessionSpec, error) {
	if strings.TrimSpace(in.APIKey) == "" {
		return nil, errors.New("OPENAI_API_KEY is required for the gptlive pipeline")
	}
	voice, accent, rate := "marin", "default", 16000
	if g := in.Metadata.GPTLive; g != nil {
		if gptLiveVoices[strings.ToLower(g.Voice)] {
			voice = strings.ToLower(g.Voice)
		}
		if g.Accent == "indian" {
			accent = "indian"
		}
		if g.Rate == 24000 {
			rate = 24000
		}
	}
	var quiz *livekit.QuizTracker
	memoType := gptLiveMemoTypeFor(in.CharacterName)
	if in.QuizBatch != nil && memoType != "" {
		cfg := in.QuizReporters
		cfg.Batch, cfg.Workspace, cfg.MemoType = in.QuizBatch, in.Workspace, memoType
		quiz = livekit.NewQuizTracker(cfg)
	}
	persona := livekit.BuildGPTLivePersona(livekit.GPTLivePersonaInput{
		Workspace: in.Workspace, CharacterName: in.CharacterName, GreetingPrompt: in.GreetingPrompt,
		LanguageName: in.LanguageName, Accent: accent, BankBlock: in.BankBlock, HasQuiz: quiz != nil,
	})
	return &livekit.GPTLiveSessionSpec{
		APIKey: in.APIKey, Voice: voice, SampleRate: rate, BackendModel: "gpt-5.6-luna",
		Persona: persona, Tools: livekit.BuildGPTLiveTools(in.BaseTools, in.Workspace, quiz), Quiz: quiz,
		WebSearch: true, MaxSessionDuration: 55 * time.Minute,
	}, nil
}
```

Use the real name of the metadata struct type in place of `roomMetadata` (read it at the top of `bootstrap_metadata.go`).

- [ ] **Step 4: Wire main.go**

In `bridgeFactory`, where `sessionCharacterName` is resolved (line ~1284) and before `return livekit.NewRoomSession(...)`:

```go
var gptLiveSpec *livekit.GPTLiveSessionSpec
if strings.EqualFold(strings.TrimSpace(os.Getenv("PICOCLAW_LIVEKIT_PIPELINE")), "gptlive") {
	spec, err := buildGPTLiveSpec(gptLiveSpecInput{
		APIKey: os.Getenv("OPENAI_API_KEY"), Metadata: bootstrap.Metadata, Workspace: workspacePath,
		CharacterName: sessionCharacterName, GreetingPrompt: personaGreeting, LanguageName: sessionLanguagePolicy.DisplayName,
		BaseTools: agentInstance.Tools, QuizBatch: quizBatchForSession, BankBlock: bankBlockForSession,
		QuizReporters: livekit.QuizTrackerConfig{
			AnswerReporter:  livekit.NewQuizAnswerReporter(lkCfg.ManagerAPI, managerServiceKey, deviceMAC, quizBatchBank(quizBatchForSession)),
			AttemptReporter: livekit.NewQuizAttemptReporter(lkCfg.ManagerAPI, managerServiceKey, deviceMAC, quizBatchBank(quizBatchForSession)),
			WonderReporter:  livekit.NewWonderQuestionReporter(lkCfg.ManagerAPI, managerServiceKey, deviceMAC),
		},
	})
	if err != nil {
		return nil, err
	}
	gptLiveSpec = spec
	sessionTTSSampleRate = spec.SampleRate // the local track must match the model's output rate
}
```

`bankBlockForSession` is `livekit.RenderQuizQuestions("{{QUIZ_QUESTIONS}}", quizBatchForSession)` when the batch is non-nil, else `livekit.RenderContentBank(...)` output already computed for content characters, else empty. The variable names `bootstrap`, `workspacePath`, `personaGreeting`, `agentInstance`, `quizBatchForSession`, `managerServiceKey`, `deviceMAC` are the ones the closure already holds; match the exact identifiers in `main.go` when applying (search each with `grep -n`). Then pass `GPTLive: gptLiveSpec` in `RoomSessionConfig`.

When the pipeline is `gptlive`, skip building the STT/TTS providers for the session only if that is a cheap change; otherwise leave them constructed and unused.

- [ ] **Step 5: Write `persistGPTLiveSession` in room_session.go**

```go
func (rs *RoomSession) persistGPTLiveSession() {
	if rs == nil || rs.gptlive == nil || strings.TrimSpace(rs.managerAPIURL) == "" || strings.TrimSpace(rs.deviceMAC) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	messages := rs.gptlive.TranscriptSnapshot()
	if err := rs.sendChatHistory(ctx, messages); err != nil {
		logger.WarnCF("livekit", "gptlive: chat history upload failed", map[string]any{"error": err.Error()})
	}
	if err := rs.sendSessionEnd(ctx, len(messages)); err != nil {
		logger.WarnCF("livekit", "gptlive: session end failed", map[string]any{"error": err.Error()})
	}
	if err := rs.sendUsageSummary(ctx, UsageSnapshot{SessionDurationSeconds: rs.gptlive.VoiceSeconds(), TotalTokens: rs.gptlive.BackendTokens()}); err != nil {
		logger.WarnCF("livekit", "gptlive: usage summary failed", map[string]any{"error": err.Error()})
	}
}
```

- [ ] **Step 6: Run tests and build**

Run: `go build ./... && go test ./cmd/picoclaw-livekit/ -run TestGPTLive -count=1 && go test ./pkg/livekit/ -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/picoclaw-livekit/bootstrap_metadata.go cmd/picoclaw-livekit/gptlive_spec.go cmd/picoclaw-livekit/gptlive_spec_test.go cmd/picoclaw-livekit/main.go pkg/livekit/room_session.go
git commit -m "feat(picoclaw-livekit): select the GPT-Live pipeline per process and persist its sessions"
```

---

### Task 14: Dev box deployment and end-to-end check

**Files:**
- Modify: `deploy/dev/README.md` (add the pm2 app under "What Runs Here" and a "Deploy: picoclaw-gptlive" section)

- [ ] **Step 1: Build on the box**

```bash
ssh root@64.227.170.31 'cd /root/picoclaw && git fetch origin && git checkout feat/gpt-live-go && export PATH=$PATH:/usr/local/go/bin && export CGO_LDFLAGS="-lc++ -lc++abi" && make build-livekit'
```

- [ ] **Step 2: Stop the Python worker and start the Go one under the same agent name**

The Python `cheeko-gptlive` pm2 app registers the same agent name; two workers would split dispatches.

```bash
ssh root@64.227.170.31 'pm2 stop cheeko-gptlive; cd /root/picoclaw && PICOCLAW_LIVEKIT_PIPELINE=gptlive OPENAI_API_KEY=$(grep ^OPENAI_API_KEY= /root/xiaozhi-esp32-server/main/python-agent/.env | cut -d= -f2-) pm2 start build/picoclaw-livekit --name picoclaw-gptlive --time -- --agent-name cheeko-gptlive --config /root/.picoclaw/config.json --log-level debug && pm2 save'
```

- [ ] **Step 3: Verify registration**

```bash
ssh root@64.227.170.31 'pm2 logs picoclaw-gptlive --lines 40 --nostream | grep -E "registered|agent_name|error"'
```
Expected: a registration line with `cheeko-gptlive` and no error.

- [ ] **Step 4: Verify from the dashboard**

Open the dev admin dashboard, GPT-Live tab, agent `cheeko-gptlive`, voice `vesper`, accent Indian English, Start. Expected: greeting within 5 s, transcripts for both sides, state pill moving listening → speaking, "what time is it" answered via the tool. Then with a Quizzy character (set the MAC to a device whose character is Quizzy): one question asked, a wrong answer produces the two-way choice, a right answer moves on, and `memory/state/daily_quiz.md` in that workspace holds the MEMO line.

- [ ] **Step 5: Document and commit**

Add to `deploy/dev/README.md` under "What Runs Here": `| picoclaw-gptlive | picoclaw | /root/picoclaw (PICOCLAW_LIVEKIT_PIPELINE=gptlive, agent cheeko-gptlive) |` and a section with the exact commands from Steps 1-3.

```bash
git add deploy/dev/README.md
git commit -m "docs(deploy): picoclaw-gptlive pm2 app on the dev box"
```

---

## Self-review

- **Spec coverage.** ADR decision 1 → Tasks 1-7. Decision 2 → Tasks 12-13. Decision 3 → Task 12 step 4 (data messages ignored). Decision 4 → Task 11. Decision 5 → Tasks 8-10 and the Tools section. Decision 6 → Task 13 step 5. Deployment → Task 14.
- **Known gaps, deliberately deferred:** `content_next` tool (content characters use the bank block in instructions); device-side playback cut on interruption; `client` delegation; skipping STT/TTS construction for GPT-Live sessions in `main.go`.
- **Type consistency.** `gptlive.Config.Tools` is a `ToolExecutor`; `NewRegistryExecutor` returns one. `QuizTracker.Score` returns `(string, error)` and `quizScoreAnswerTool` maps the error to `tools.ErrorResult`. `GPTLiveSessionSpec.Voice` is `any` to allow a custom voice map; the metadata path only ever sets a string. `PersistedChatMessage` fields are `ChatType, Content, Timestamp`.
- **Verification left to the executor.** The exact identifier names inside `main.go`'s `bridgeFactory` closure (Task 13 step 4) and the `ChatType` numbers (Task 12) must be read from the code, not assumed.
