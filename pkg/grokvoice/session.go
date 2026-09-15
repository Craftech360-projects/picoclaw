// Package grokvoice speaks xAI's Grok Voice Agent API (OpenAI Realtime-style events,
// https://docs.x.ai/docs/guides/voice/agent) and emits gptlive events, so gptLivePipeline can run
// a session on it. Grok is a single model: it calls the session's tools itself.
package grokvoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/realtimeconn"
)

const (
	DefaultBaseURL = "wss://api.x.ai/v1/realtime"
	DefaultModel   = "grok-voice-think-fast-1.0"
	DefaultVoice   = "ara"
	SampleRate     = 24000 // PCM16 mono, both directions
	defaultSilence = 700 * time.Millisecond
)

type Config struct {
	APIKey       string
	BaseURL      string // default DefaultBaseURL
	Model        string // default DefaultModel
	Voice        string // default DefaultVoice
	Instructions string
	Tools        []map[string]any     // gptlive.FunctionTools shape; {"type":"web_search"} passes through
	Executor     gptlive.ToolExecutor // nil: every call is answered with an error output
	Silence      time.Duration        // server VAD end-of-turn silence; default 700ms
}

type Session struct {
	*realtimeconn.Conn
	cfg     Config
	started time.Time

	mu           sync.Mutex
	instructions string
	agentText    map[string]string
	resp         *respState // in-flight response's tool calls; nil when none are owed a continuation
}

// respState tracks one response's tool calls: which ones it made
// (response.function_call_arguments.done) and which have returned an output
// (answer's conversation.item.create). maybeContinue sends the single
// response.create that resumes the response once both response.done has
// arrived and every call in calls has a matching entry in returned — never
// per-call, and never before response.done, matching the pattern
// gptlive/delegation.go uses for its own backend continuations.
type respState struct {
	done     bool
	calls    map[string]bool
	returned map[string]bool
}

func Dial(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("grokvoice: API key is empty")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Voice == "" {
		cfg.Voice = DefaultVoice
	}
	if cfg.Silence <= 0 {
		cfg.Silence = defaultSilence
	}
	ws, err := realtimeconn.Dial(ctx, cfg.BaseURL+"?model="+url.QueryEscape(cfg.Model),
		http.Header{"Authorization": {"Bearer " + cfg.APIKey}}, cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("grokvoice: %w", err)
	}
	s := &Session{Conn: realtimeconn.New(ws), cfg: cfg, started: time.Now(), instructions: cfg.Instructions, agentText: map[string]string{}}
	if err := s.Send(s.sessionUpdate()); err != nil {
		_ = s.Conn.Close()
		return nil, fmt.Errorf("grokvoice: session.update: %w", err)
	}
	go s.Run(s.handle, nil, s.onEnd)
	return s, nil
}

func (s *Session) sessionUpdate() map[string]any {
	s.mu.Lock()
	instructions := s.instructions
	s.mu.Unlock()
	pcm := map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": SampleRate}}
	session := map[string]any{
		"voice":          s.cfg.Voice,
		"instructions":   instructions,
		"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": s.cfg.Silence.Milliseconds()},
		"audio":          map[string]any{"input": pcm, "output": pcm},
	}
	if len(s.cfg.Tools) > 0 {
		session["tools"] = s.cfg.Tools
	}
	return map[string]any{"type": "session.update", "session": session}
}

func (s *Session) PushAudio(pcm []byte) {
	_ = s.Send(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)})
}

// AppendInstructions grows the standing instructions (the quiz Door directive) and re-sends them.
func (s *Session) AppendInstructions(text string) {
	s.mu.Lock()
	s.instructions += "\n\n" + text
	s.mu.Unlock()
	_ = s.Send(s.sessionUpdate())
}

// AppendCommentary makes the model say something now (greeting, goodbye).
func (s *Session) AppendCommentary(text string) {
	_ = s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}},
	}})
	s.requestResponse()
}

// requestResponse sends response.create unless a tool-call continuation is still
// owed one for the response in flight (s.resp != nil): the protocol rejects a
// second response.create while one is already active, and maybeContinue will
// send its own response.create — which picks up whatever AppendCommentary just
// queued — as soon as that response is done and every call has returned.
func (s *Session) requestResponse() {
	s.mu.Lock()
	owed := s.resp != nil
	s.mu.Unlock()
	if owed {
		return
	}
	_ = s.Send(map[string]any{"type": "response.create"})
}

func (s *Session) Close(ctx context.Context) error {
	_ = s.Conn.Close()
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type serverEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	ItemID     string `json:"item_id"`
	Transcript string `json:"transcript"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Response   *struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

func (s *Session) handle(raw []byte) {
	var ev serverEvent
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch ev.Type {
	case "response.output_audio.delta", "response.audio.delta":
		if pcm, err := base64.StdEncoding.DecodeString(ev.Delta); err == nil {
			s.EmitAudio(pcm)
		}
	case "input_audio_buffer.speech_started":
		s.Emit(gptlive.Interrupted{})
	case "conversation.item.input_audio_transcription.completed":
		if ev.Transcript != "" {
			s.Emit(gptlive.UserTranscript{ID: ev.ItemID, Text: ev.Transcript, Final: true, StartedAt: time.Now()})
		}
	case "response.output_audio_transcript.delta":
		s.mu.Lock()
		s.agentText[ev.ItemID] += ev.Delta
		text := s.agentText[ev.ItemID]
		s.mu.Unlock()
		s.Emit(gptlive.AgentTranscript{ID: ev.ItemID, Delta: ev.Delta, Text: text})
	case "response.function_call_arguments.done":
		call := gptlive.FunctionCall{CallID: ev.CallID, Name: ev.Name, Arguments: ev.Arguments}
		s.mu.Lock()
		if s.resp == nil {
			s.resp = &respState{calls: map[string]bool{}, returned: map[string]bool{}}
		}
		s.resp.calls[ev.CallID] = true
		s.mu.Unlock()
		s.Emit(call)
		s.Go(func() { s.answer(call) })
	case "response.done":
		if ev.Response != nil && ev.Response.Usage != nil {
			u := ev.Response.Usage
			s.Emit(gptlive.BackendUsage{Model: s.cfg.Model, Input: u.InputTokens, Output: u.OutputTokens, Total: u.TotalTokens})
		}
		s.Emit(gptlive.VoiceUsage{Seconds: time.Since(s.started).Seconds()})
		s.mu.Lock()
		// The response (and every item in it) is finished: clear the accumulated
		// per-item transcript text now rather than letting it grow for the life
		// of the session, and rather than letting a missing item_id accumulate
		// every response's deltas under the same "" key.
		s.agentText = map[string]string{}
		if s.resp != nil {
			s.resp.done = true
		}
		s.mu.Unlock()
		s.maybeContinue()
	case "error":
		s.Emit(gptlive.Error{Err: fmt.Errorf("grokvoice: %s", ev.Error), Recoverable: true})
	}
}

func (s *Session) answer(call gptlive.FunctionCall) {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &args)
	output, isErr := realtimeconn.RunTool(s.cfg.Executor, call.Name, args)
	s.Emit(gptlive.FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	_ = s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": call.CallID, "output": output,
	}})
	s.mu.Lock()
	if s.resp != nil {
		s.resp.returned[call.CallID] = true
	}
	s.mu.Unlock()
	s.maybeContinue()
}

// maybeContinue sends the single response.create that resumes a response after
// its tool calls are answered, once response.done has arrived AND every call
// response.function_call_arguments.done reported for it has returned an output.
// Sending response.create as each tool finished (the previous behavior) could
// fire while the response was still open — the protocol rejects a
// response.create sent while one is already active — and fired once per call,
// which is a duplicate whenever a response makes more than one call. Matches
// the pattern gptlive/delegation.go's maybeContinue uses for the same problem
// on the GPT-Live backend.
func (s *Session) maybeContinue() {
	s.mu.Lock()
	r := s.resp
	if r == nil || !r.done {
		s.mu.Unlock()
		return
	}
	for id := range r.calls {
		if !r.returned[id] {
			s.mu.Unlock()
			return
		}
	}
	s.resp = nil
	s.mu.Unlock()
	_ = s.Send(map[string]any{"type": "response.create"})
}

// onEnd: Grok has no session resumption here, so an unexpected socket end ends the session
// (gptLivePipeline leaves the room on a non-recoverable Error).
func (s *Session) onEnd(err error) {
	if !s.Closing() {
		s.Emit(gptlive.Error{Err: fmt.Errorf("grokvoice: connection lost: %v", err), Recoverable: false})
	}
	s.Emit(gptlive.Closed{Reason: "socket closed", VoiceSeconds: time.Since(s.started).Seconds()})
}
