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
	"github.com/sipeed/picoclaw/pkg/logger"
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

	// active, pending and owed together decide when the single response.create
	// that resumes the conversation may be sent. They are tracked globally
	// (not per response_id) because only one continuation is ever needed
	// regardless of how many responses or calls are outstanding at once — see
	// maybeContinue's own comment for why that is still correct across an
	// interruption (a later response starting while an earlier one's tool call
	// is still running).
	active  bool            // a response is in flight: response.created, or a call within one, seen; response.done not yet seen
	pending map[string]bool // call IDs from response.function_call_arguments.done not yet answered
	owed    bool            // a tool output or AppendCommentary is waiting on a response.create

	logLimit *realtimeconn.LogLimiter // unknown/unparsed events
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
	s := &Session{Conn: realtimeconn.New(ws), cfg: cfg, started: time.Now(), instructions: cfg.Instructions,
		agentText: map[string]string{}, pending: map[string]bool{}, logLimit: realtimeconn.NewLogLimiter(30 * time.Second)}
	s.SetSecret(cfg.APIKey)
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

// AppendCommentary makes the model say something now (greeting, goodbye). The
// item is queued immediately — that's always safe — but the response.create
// that makes the model actually speak to it goes through the same maybeContinue
// gate as a tool continuation, so it waits out a response that's already
// active instead of being rejected mid-speech.
func (s *Session) AppendCommentary(text string) {
	_ = s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}},
	}})
	s.mu.Lock()
	s.owed = true
	s.mu.Unlock()
	s.maybeContinue()
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
		if ok, held := s.logLimit.Allow("unparsed"); ok {
			logger.WarnCF("realtime", "grok voice: unparsed server event dropped", map[string]any{"bytes": len(raw), "suppressed": held})
		}
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
	case "response.created":
		s.mu.Lock()
		s.active = true
		s.mu.Unlock()
	case "response.function_call_arguments.done":
		call := gptlive.FunctionCall{CallID: ev.CallID, Name: ev.Name, Arguments: ev.Arguments}
		s.mu.Lock()
		s.active = true // a call only ever happens inside a response; covers a vendor that omits response.created
		s.pending[ev.CallID] = true
		s.mu.Unlock()
		s.Emit(call)
		s.Go(func() { s.answer(call) })
	case "response.done":
		// usage is optional in xAI's own schema and the live server omits it, which
		// is invisible from the outside (it just looks like a zero-token session).
		// response.done is the one event whose schema carries no transcript and no
		// audio, so the raw frame is safe to show — truncated, scrubbed of the API
		// key by Conn.Scrub (SetSecret is wired in Dial), and rate-limited.
		if ok, held := s.logLimit.Allow("response.done"); ok {
			fields := map[string]any{
				"has_response": ev.Response != nil,
				"has_usage":    ev.Response != nil && ev.Response.Usage != nil,
				"suppressed":   held,
			}
			if ev.Response == nil || ev.Response.Usage == nil {
				fields["raw"] = s.Scrub(string(raw[:min(len(raw), 400)]))
			}
			logger.InfoCF("realtime", "grok voice: response.done", fields)
		}
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
		s.active = false
		s.mu.Unlock()
		s.maybeContinue()
	case "error":
		s.Emit(gptlive.Error{Err: fmt.Errorf("grokvoice: %s", ev.Error), Recoverable: true})
	default:
		if ignoredEvents[ev.Type] {
			return
		}
		typ := ev.Type
		if len(typ) > 64 {
			typ = typ[:64]
		}
		if ok, held := s.logLimit.Allow("type:" + typ); ok { // the type name only: payloads carry transcript text
			logger.InfoCF("realtime", "grok voice: unhandled server event type", map[string]any{"type": typ, "bytes": len(raw), "suppressed": held})
		}
	}
}

// ignoredEvents are documented Voice Agent events handle deliberately does nothing with, so the
// "unhandled server event type" line shows only what is actually unexpected.
var ignoredEvents = map[string]bool{
	"session.created": true, "session.updated": true, "conversation.created": true,
	"conversation.item.added": true, "conversation.item.created": true,
	"input_audio_buffer.speech_stopped": true, "input_audio_buffer.committed": true, "input_audio_buffer.cleared": true,
	"response.output_item.added": true, "response.output_item.done": true,
	"response.content_part.added": true, "response.content_part.done": true,
	"response.output_audio.done": true, "response.audio.done": true, "response.output_audio_transcript.done": true,
	"response.function_call_arguments.delta": true, "ping": true,
}

func (s *Session) answer(call gptlive.FunctionCall) {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &args)
	start := time.Now()
	output, isErr := realtimeconn.RunTool(s.cfg.Executor, call.Name, args)
	toolMs := time.Since(start).Milliseconds()
	s.Emit(gptlive.FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	sendErr := s.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": call.CallID, "output": output,
	}})
	logger.InfoCF("realtime", "grok voice: tool result", map[string]any{
		"name": call.Name, "is_error": isErr, "ms": toolMs, "output_bytes": len(output), "response_sent": sendErr == nil,
	})
	s.mu.Lock()
	delete(s.pending, call.CallID)
	s.owed = true
	s.mu.Unlock()
	s.maybeContinue()
}

// maybeContinue sends the single response.create that resumes the conversation,
// once no response is active, no call is still outstanding, and at least one
// has returned an output (or AppendCommentary queued something) since the last
// continuation was sent. active/pending/owed are a single running total, not
// one entry per response_id as gptlive/delegation.go's maybeContinue keeps for
// the GPT-Live backend, because only one continuation is ever needed regardless
// of how many responses or calls are outstanding — which also makes this
// correct across an interruption: a slow tool call from response A returning
// while the child's own speech has already started response B defers the
// create (active is still true, from B) instead of firing it mid-response B
// (rejected by the protocol) or losing track of A's call once B's bookkeeping
// would otherwise have overwritten it.
func (s *Session) maybeContinue() {
	s.mu.Lock()
	if s.active || len(s.pending) > 0 || !s.owed {
		s.mu.Unlock()
		return
	}
	s.owed = false
	s.mu.Unlock()
	_ = s.Send(map[string]any{"type": "response.create"})
}

// onEnd: Grok has no session resumption here, so an unexpected socket end ends the session
// (gptLivePipeline leaves the room on a non-recoverable Error).
func (s *Session) onEnd(err error) {
	if !s.Closing() {
		s.Emit(gptlive.Error{Err: errors.New("grokvoice: connection lost: " + s.Scrub(fmt.Sprint(err))), Recoverable: false})
	}
	s.Emit(gptlive.Closed{Reason: "socket closed", VoiceSeconds: time.Since(s.started).Seconds()})
}
