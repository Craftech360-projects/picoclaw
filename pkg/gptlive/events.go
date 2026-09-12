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
	EventSessionStart       = "session.start"
	EventSessionUpdate      = "session.update"
	EventInputAudioAppend   = "session.input_audio.append"
	EventInputAudioMute     = "session.input_audio.mute"
	EventInputAudioUnmute   = "session.input_audio.unmute"
	EventInstructionsAppend = "session.instructions.append"
	EventThinkingAppend     = "session.thinking.append"
	EventCommentaryAppend   = "session.commentary.append"
	EventResponseItemCreate = "response.item.create"
	EventResponseCreate     = "response.create"
	EventSessionClose       = "session.close"
)

// Server event types.
const (
	EventSessionStarted        = "session.started"
	EventOutputAudioDelta      = "session.output_audio.delta"
	EventInputTranscriptDelta  = "session.input_transcript.delta"
	EventOutputTranscriptDelta = "session.output_transcript.delta"
	EventDelegationCreated     = "session.delegation.created"
	EventResponseEvent         = "response.event"
	EventSessionUsageUpdated   = "session.usage.updated"
	EventSessionClosed         = "session.closed"
	EventError                 = "error"
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
