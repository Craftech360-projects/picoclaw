// Package geminilive speaks Google's Gemini Live API (BidiGenerateContent websocket,
// https://ai.google.dev/api/live) and emits gptlive events, so gptLivePipeline can run a session
// on it. Gemini is a single model: it calls the session's tools itself. Mic input is 16 kHz.
package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/realtimeconn"
)

const (
	DefaultBaseURL   = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	DefaultModel     = "gemini-2.5-flash-native-audio-preview-12-2025"
	DefaultVoice     = "Puck"
	InputSampleRate  = 16000 // the Live API accepts only 16 kHz PCM input
	OutputSampleRate = 24000
	defaultSilence   = 700 * time.Millisecond
	setupTimeout     = 10 * time.Second
)

type Config struct {
	APIKey       string
	BaseURL      string // default DefaultBaseURL
	Model        string // default DefaultModel
	Voice        string // default DefaultVoice
	Instructions string
	Tools        []map[string]any     // gptlive.FunctionTools shape; {"type":"web_search"} becomes Google Search
	Executor     gptlive.ToolExecutor // nil: every call is answered with an error output
	Silence      time.Duration        // automatic activity detection end-of-turn silence; default 700ms
}

type Session struct {
	*realtimeconn.Conn
	cfg     Config
	started time.Time

	mu        sync.Mutex
	handle    string // latest resumable session handle
	turn      int
	userText  string
	agentText string
	warnedDir bool
	// diagnostics (logged, never sent): see the "gemini live:" log lines
	stats          turnStats
	socketOpened   time.Time // setupComplete on the current socket
	resumableKnown bool
	resumable      bool
	logLimit       *realtimeconn.LogLimiter // unknown/unparsed frames
	usageLimit     *realtimeconn.LogLimiter // the usage line: at most once a second, so every turn still logs
	lastUsage      [6]int                   // the usage line is logged only when these counts change
	// turnUsage is the largest usageMetadata this turn has already reported to the
	// pipeline; only the growth over it is emitted, so a usageMetadata repeated
	// within a turn cannot inflate the persisted totals. Reset at every turn end.
	// See the usageMetadata branch of handleMessage.
	turnUsage gptlive.BackendUsage
	// pendingTools are toolResponses whose socket died before they could be
	// written; they are replayed on the socket that replaces it. See answer and
	// flushToolResponses. hasPendingTools mirrors len(pendingTools) > 0 so the read
	// loop can check it on every frame without taking mu.
	pendingTools    []pendingTool
	flushingTools   bool
	hasPendingTools atomic.Bool
	// flushTimer is started by the current turn's first model output and flushes the user
	// transcript userFlushDelay later if the turn has not ended by then; nil until that output
	flushTimer *time.Timer
}

// userFlushDelay bounds how long the user transcript waits for the model turn to end. Server-side
// work (Google Search, a slow tool) can pause the model long enough for the pipeline to close the
// agent's audio burst and persist its text; the user message must be out before that
// (segmentIdle 800ms plus playout lead). A var so tests can pin it.
var userFlushDelay = 500 * time.Millisecond

func Dial(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("geminilive: API key is empty")
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
	s := &Session{cfg: cfg, started: time.Now(), logLimit: realtimeconn.NewLogLimiter(30 * time.Second),
		usageLimit: realtimeconn.NewLogLimiter(time.Second)}
	ws, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	s.Conn = realtimeconn.New(ws)
	s.SetSecret(cfg.APIKey)
	go s.Run(s.handleMessage, s.redial, s.onEnd)
	return s, nil
}

// connect dials, sends setup (carrying the resumption handle once there is one) and waits for setupComplete.
func (s *Session) connect(ctx context.Context) (*websocket.Conn, error) {
	// the key rides in the URL (Gemini's only API-key auth): never log this string
	escaped := url.QueryEscape(s.cfg.APIKey)
	dialStart := time.Now()
	s.mu.Lock()
	resumed := s.handle != ""
	s.mu.Unlock()
	ws, err := realtimeconn.Dial(ctx, s.cfg.BaseURL+"?key="+escaped, nil, s.cfg.APIKey)
	if err != nil {
		// Dial scrubs the raw key; the URL carries the escaped form, which differs for keys with + / = etc.
		return nil, errors.New("geminilive: " + strings.ReplaceAll(err.Error(), escaped, "***"))
	}
	if err := ws.WriteJSON(s.setup()); err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("geminilive: setup: %w", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(setupTimeout))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			_ = ws.Close()
			return nil, fmt.Errorf("geminilive: waiting for setupComplete (check model name and key): %w", err)
		}
		var m struct {
			SetupComplete *struct{} `json:"setupComplete"`
		}
		if json.Unmarshal(data, &m) == nil && m.SetupComplete != nil {
			break
		}
	}
	_ = ws.SetReadDeadline(time.Time{})
	decls, search := toolCounts(s.cfg.Tools)
	logger.InfoCF("realtime", "gemini live: setupComplete", map[string]any{
		"setup_ms": msSince(dialStart), "resumed": resumed, "model": s.cfg.Model,
		"instructions_bytes": len(s.cfg.Instructions), "function_decls": decls, "google_search": search,
	})
	s.mu.Lock()
	s.socketOpened = time.Now()
	s.mu.Unlock()
	// A toolResponse whose socket died is replayed here, on the new socket, as soon
	// as it is set up. Writing to ws directly is what makes that possible: Run has
	// not swapped it into the Conn yet, so s.Send would still address the dead one —
	// and for the same reason nothing else can be writing to ws at this point.
	s.flushToolResponses(func(v any) error { return ws.WriteJSON(v) })
	return ws, nil
}

func (s *Session) redial() (*websocket.Conn, error) {
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	if handle == "" {
		return nil, errors.New("geminilive: connection ended with no resumable session handle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.connect(ctx)
}

func (s *Session) setup() map[string]any {
	s.mu.Lock()
	resumption := map[string]any{}
	if s.handle != "" {
		resumption["handle"] = s.handle
	}
	s.mu.Unlock()
	setup := map[string]any{
		"model": "models/" + s.cfg.Model,
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": s.cfg.Voice}}},
		},
		"systemInstruction":        map[string]any{"parts": []map[string]any{{"text": s.cfg.Instructions}}},
		"realtimeInputConfig":      map[string]any{"automaticActivityDetection": map[string]any{"silenceDurationMs": s.cfg.Silence.Milliseconds()}},
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
		// audio-only sessions stop at 15 minutes without compression; connections at ~10 minutes, hence resumption
		"contextWindowCompression": map[string]any{"slidingWindow": map[string]any{}},
		"sessionResumption":        resumption,
	}
	if disableThinkingBudget(s.cfg.Model) {
		setup["generationConfig"].(map[string]any)["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
	}
	if tools := liveTools(s.cfg.Tools); len(tools) > 0 {
		setup["tools"] = tools
	}
	return map[string]any{"setup": setup}
}

// liveTools converts flat function tools to Live API tools. parametersJsonSchema takes the JSON
// Schema the tool registry already produces, so no OpenAPI-subset rewriting is needed.
func liveTools(defs []map[string]any) []map[string]any {
	var decls []map[string]any
	search := false
	for _, d := range defs {
		switch d["type"] {
		case "web_search":
			search = true
		case "function":
			decls = append(decls, map[string]any{"name": d["name"], "description": d["description"], "parametersJsonSchema": d["parameters"]})
		}
	}
	var out []map[string]any
	if len(decls) > 0 {
		out = append(out, map[string]any{"functionDeclarations": decls})
	}
	if search {
		out = append(out, map[string]any{"googleSearch": map[string]any{}})
	}
	return out
}

func (s *Session) PushAudio(pcm []byte) {
	_ = s.Send(map[string]any{"realtimeInput": map[string]any{
		"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(pcm), "mimeType": "audio/pcm;rate=16000"},
	}})
}

// AppendInstructions is a no-op: Gemini Live has no mid-session instruction update (3.1 rejects
// clientContent after setup outright). The quiz Door directive reaches the model in the
// quiz_score_answer tool result instead.
func (s *Session) AppendInstructions(string) {
	s.mu.Lock()
	warn := !s.warnedDir
	s.warnedDir = true
	s.mu.Unlock()
	if warn {
		logger.InfoCF("realtime", "gemini live: mid-session instructions are not sent; the tool result carries the directive", nil)
	}
}

// isGemini31 reports a Gemini 3.1 Live model (the same substring check the LiveKit plugin uses).
func isGemini31(model string) bool { return strings.Contains(model, "3.1") }

// disableThinkingBudget reports a 2.5 Flash model, whose setup turns thinking off with
// generationConfig.thinkingConfig.thinkingBudget 0: thinking cost seconds before the first audio.
// Scoped to Flash, not all of 2.5: a 2.5 Pro Live model's minimum thinking budget is 128, so
// sending 0 would make it refuse the setup.
// 3.1 takes thinkingLevel instead of thinkingBudget and already defaults to "minimal", so it is left alone.
func disableThinkingBudget(model string) bool { return strings.Contains(model, "2.5-flash") }

// AppendCommentary makes the model say something now. 3.1 models take realtime text;
// 2.5 models take a completed user turn.
func (s *Session) AppendCommentary(text string) {
	if isGemini31(s.cfg.Model) {
		_ = s.Send(map[string]any{"realtimeInput": map[string]any{"text": text}})
		return
	}
	_ = s.Send(map[string]any{"clientContent": map[string]any{
		"turns": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": text}}}}, "turnComplete": true,
	}})
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

type serverMessage struct {
	ServerContent *struct {
		ModelTurn *struct {
			Parts []struct {
				InlineData *struct {
					Data string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		Interrupted        bool `json:"interrupted"`
		TurnComplete       bool `json:"turnComplete"`
		GenerationComplete bool `json:"generationComplete"`
		InputTranscription *struct {
			Text string `json:"text"`
		} `json:"inputTranscription"`
		OutputTranscription *struct {
			Text string `json:"text"`
		} `json:"outputTranscription"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []struct {
			ID   string         `json:"id"`
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"functionCalls"`
	} `json:"toolCall"`
	ToolCallCancellation *struct {
		IDs []string `json:"ids"`
	} `json:"toolCallCancellation"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		ResponseTokenCount      int `json:"responseTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	SessionResumptionUpdate *struct {
		NewHandle string `json:"newHandle"`
		Resumable bool   `json:"resumable"`
	} `json:"sessionResumptionUpdate"`
	GoAway json.RawMessage `json:"goAway"`
}

func (s *Session) handleMessage(raw []byte) {
	var m serverMessage
	if json.Unmarshal(raw, &m) != nil {
		if ok, held := s.logLimit.Allow("unparsed"); ok {
			logger.WarnCF("realtime", "gemini live: unparsed server message dropped", map[string]any{"bytes": len(raw), "suppressed": held})
		}
		return
	}
	s.logUnknownKeys(raw)
	// A frame means this socket is live. If a toolResponse was queued after the
	// resume had already flushed the queue (the tool returned a moment too late),
	// this is what gets it out — off the read goroutine, so the write can never
	// stall the read loop.
	if s.hasPendingTools.Load() {
		s.Go(func() { s.flushToolResponses(s.Send) })
	}
	if c := m.ServerContent; c != nil {
		if c.InputTranscription != nil {
			s.mu.Lock()
			s.userText += c.InputTranscription.Text
			s.stats.input(time.Now(), len(c.InputTranscription.Text))
			s.mu.Unlock()
		}
		if c.ModelTurn != nil || c.OutputTranscription != nil {
			s.mu.Lock()
			if s.flushTimer == nil {
				turn := s.turn
				s.flushTimer = time.AfterFunc(userFlushDelay, func() {
					s.Go(func() { s.flushTurn(turn) }) // Go: tracked by Run, and refused once the session is ending
				})
			}
			s.mu.Unlock()
		}
		if c.ModelTurn != nil {
			for _, p := range c.ModelTurn.Parts {
				if p.InlineData == nil {
					continue
				}
				if pcm, err := base64.StdEncoding.DecodeString(p.InlineData.Data); err == nil {
					s.mu.Lock()
					s.stats.audio(time.Now())
					s.mu.Unlock()
					s.EmitAudio(pcm)
				}
			}
		}
		if t := c.OutputTranscription; t != nil && t.Text != "" {
			s.mu.Lock()
			s.agentText += t.Text
			ev := gptlive.AgentTranscript{ID: fmt.Sprintf("gemini-agent-%d", s.turn), Delta: t.Text, Text: s.agentText}
			s.mu.Unlock()
			s.Emit(ev)
		}
		// One final user transcript per turn, flushed when the model turn ends or userFlushDelay after
		// its first output, whichever comes first: input transcription often trails the model's first
		// audio, and the pipeline persists the agent's text only when its audio burst closes.
		if c.GenerationComplete {
			s.mu.Lock()
			s.stats.generationComplete = true
			s.mu.Unlock()
		}
		if c.GenerationComplete || c.TurnComplete || c.Interrupted {
			s.stopFlushTimer()
			s.flushUser()
		}
		if c.Interrupted {
			s.Emit(gptlive.Interrupted{})
		}
		if c.Interrupted || c.TurnComplete {
			s.mu.Lock()
			fields := s.stats.end(time.Now(), s.turn, c.Interrupted)
			s.mu.Unlock()
			logger.InfoCF("realtime", "gemini live: turn", fields)
			s.mu.Lock()
			s.turn++
			s.agentText = ""
			s.flushTimer = nil
			s.turnUsage = gptlive.BackendUsage{} // the next turn's usage is its own, not a delta on this one
			s.mu.Unlock()
		}
	}
	if tc := m.ToolCall; tc != nil {
		for _, fc := range tc.FunctionCalls {
			args, _ := json.Marshal(fc.Args)
			call := gptlive.FunctionCall{CallID: fc.ID, Name: fc.Name, Arguments: string(args)}
			s.Emit(call)
			fcArgs := fc.Args
			s.Go(func() { s.answer(call, fcArgs) })
		}
	}
	if tc := m.ToolCallCancellation; tc != nil {
		if ok, held := s.logLimit.Allow("toolCallCancellation"); ok {
			logger.InfoCF("realtime", "gemini live: toolCallCancellation", map[string]any{"ids": len(tc.IDs), "suppressed": held})
		}
	}
	if u := m.UsageMetadata; u != nil {
		counts := [6]int{u.PromptTokenCount, u.ResponseTokenCount, u.ThoughtsTokenCount, u.ToolUsePromptTokenCount, u.CachedContentTokenCount, u.TotalTokenCount}
		s.mu.Lock()
		changed := counts != s.lastUsage
		s.lastUsage = counts
		// The pipeline SUMS every BackendUsage into the tokens it persists, and a
		// usageMetadata message carries the CURRENT TURN's totals, not a delta — so
		// a vendor that repeats usageMetadata within a turn (the limiter below
		// exists because it might) would multiply the persisted tokens by the
		// number of times it repeated. Only the growth over what this turn has
		// already emitted is emitted, which leaves a single usageMetadata per turn
		// — what the live runs show, with prompt tokens rising per turn and the
		// session total equal to the sum of the turn totals — reporting exactly
		// what it reports today, while a repeat inside the same turn adds nothing.
		// turnUsage is reset at every turn end (see the serverContent block above,
		// which runs first, so a message carrying both turnComplete and
		// usageMetadata still credits the turn that just ended in full).
		delta := gptlive.BackendUsage{
			Model:  s.cfg.Model,
			Input:  u.PromptTokenCount - s.turnUsage.Input,
			Output: u.ResponseTokenCount - s.turnUsage.Output,
			Total:  u.TotalTokenCount - s.turnUsage.Total,
		}
		s.turnUsage = gptlive.BackendUsage{Input: max(s.turnUsage.Input, u.PromptTokenCount),
			Output: max(s.turnUsage.Output, u.ResponseTokenCount), Total: max(s.turnUsage.Total, u.TotalTokenCount)}
		s.mu.Unlock()
		if changed {
			// usage normally arrives once per turn; the limiter only bites if a vendor sends it per frame
			if ok, held := s.usageLimit.Allow("usage"); ok {
				logger.InfoCF("realtime", "gemini live: usage", map[string]any{
					"prompt_tokens": u.PromptTokenCount, "response_tokens": u.ResponseTokenCount, "thoughts_tokens": u.ThoughtsTokenCount,
					"tool_use_prompt_tokens": u.ToolUsePromptTokenCount, "cached_tokens": u.CachedContentTokenCount, "total_tokens": u.TotalTokenCount,
					"suppressed": held,
				})
			}
		}
		if delta.Input > 0 || delta.Output > 0 || delta.Total > 0 {
			delta.Input, delta.Output, delta.Total = max(delta.Input, 0), max(delta.Output, 0), max(delta.Total, 0)
			s.Emit(delta)
		}
		// VoiceUsage is the session's wall clock to date, replaced (monotonically) by
		// the pipeline rather than summed, so a repeat costs nothing.
		s.Emit(gptlive.VoiceUsage{Seconds: time.Since(s.started).Seconds()})
	}
	if r := m.SessionResumptionUpdate; r != nil {
		s.mu.Lock()
		if r.Resumable && r.NewHandle != "" {
			s.handle = r.NewHandle
		}
		changed := !s.resumableKnown || s.resumable != r.Resumable
		s.resumableKnown, s.resumable = true, r.Resumable
		hasHandle := s.handle != ""
		s.mu.Unlock()
		if changed { // never the handle itself
			logger.InfoCF("realtime", "gemini live: session resumable changed", map[string]any{"resumable": r.Resumable, "has_handle": hasHandle})
		}
	}
	if len(m.GoAway) > 0 {
		var g struct {
			TimeLeft string `json:"timeLeft"`
		}
		_ = json.Unmarshal(m.GoAway, &g)
		s.mu.Lock()
		opened := s.socketOpened
		s.mu.Unlock()
		logger.InfoCF("realtime", "gemini live: goAway, the session resumes on a new connection", map[string]any{
			"time_left": g.TimeLeft, "socket_age_ms": msSince(opened),
		})
	}
}

// knownServerKeys are the top-level BidiGenerateContentServerMessage fields handleMessage reads.
var knownServerKeys = map[string]bool{
	"setupComplete": true, "serverContent": true, "toolCall": true, "toolCallCancellation": true,
	"usageMetadata": true, "sessionResumptionUpdate": true, "goAway": true,
}

// logUnknownKeys logs top-level keys handleMessage does not read (a server error shape, a new
// message type), rate-limited per key. Key names only: values can carry transcript text.
func (s *Session) logUnknownKeys(raw []byte) {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return
	}
	for k := range top {
		if knownServerKeys[k] {
			continue
		}
		if len(k) > 64 {
			k = k[:64]
		}
		if ok, held := s.logLimit.Allow("key:" + k); ok {
			logger.InfoCF("realtime", "gemini live: unhandled server message key", map[string]any{"key": k, "bytes": len(raw), "suppressed": held})
		}
	}
}

func (s *Session) stopFlushTimer() {
	s.mu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
	}
	s.mu.Unlock()
}

func (s *Session) flushUser() { s.flushTurn(-1) }

// flushTurn emits the buffered user text as one final transcript. turn >= 0 flushes only while
// that turn is still current, so a late timer can't split the next turn's utterance.
func (s *Session) flushTurn(turn int) {
	s.mu.Lock()
	if turn >= 0 && turn != s.turn {
		s.mu.Unlock()
		return
	}
	text := strings.TrimSpace(s.userText)
	s.userText = ""
	id := fmt.Sprintf("gemini-user-%d", s.turn)
	s.mu.Unlock()
	if text != "" {
		s.Emit(gptlive.UserTranscript{ID: id, Text: text, Final: true, StartedAt: time.Now()})
	}
}

func (s *Session) answer(call gptlive.FunctionCall, args map[string]any) {
	start := time.Now()
	output, isErr := realtimeconn.RunTool(s.cfg.Executor, call.Name, args)
	toolMs := msSince(start)
	s.Emit(gptlive.FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	response := map[string]any{"output": output}
	if isErr {
		response = map[string]any{"error": output}
	}
	payload := map[string]any{"toolResponse": map[string]any{"functionResponses": []map[string]any{
		{"id": call.CallID, "name": call.Name, "response": response},
	}}}
	sendErr := s.Send(payload)
	if sendErr != nil {
		// Gemini is the one vendor that redials mid-session, and Send writes to
		// whichever socket Conn currently holds: between the read error and the
		// swap, that is a dead socket. A toolResponse lost there leaves the model
		// waiting on a result that never arrives — for quiz_score_answer the quiz
		// stalls mid-question and the child gets dead air — so it is queued and
		// replayed on the socket that replaces it. Bounded: see flushToolResponses.
		s.queueToolResponse(pendingTool{name: call.Name, payload: payload, queued: time.Now()})
	}
	logger.InfoCF("realtime", "gemini live: tool result", map[string]any{
		"name": call.Name, "is_error": isErr, "ms": toolMs, "output_bytes": len(output),
		"response_sent": sendErr == nil, "queued_for_resume": sendErr != nil,
	})
}

// pendingTool is one toolResponse whose socket died before it could be written.
type pendingTool struct {
	name    string
	payload map[string]any
	queued  time.Time
}

const (
	// toolResponseReplayWindow bounds how long a queued toolResponse is worth
	// replaying. A redial dials for at most 15s and then waits up to setupTimeout
	// for setupComplete, so 30s covers a resumption with room to spare; past that
	// the model has moved on and delivering a stale quiz score would be worse than
	// dropping it. Dropped results are warned about, never silently discarded.
	toolResponseReplayWindow = 30 * time.Second
	// maxPendingToolResponses bounds the queue itself, so a socket that never comes
	// back cannot grow it without limit.
	maxPendingToolResponses = 8
)

func (s *Session) queueToolResponse(p pendingTool) {
	s.mu.Lock()
	if len(s.pendingTools) >= maxPendingToolResponses {
		dropped := s.pendingTools[0]
		s.pendingTools = s.pendingTools[1:]
		logger.WarnCF("realtime", "gemini live: tool result dropped; the replay queue is full", map[string]any{"name": dropped.name})
	}
	s.pendingTools = append(s.pendingTools, p)
	s.mu.Unlock()
	s.hasPendingTools.Store(true)
}

// flushToolResponses writes every queued toolResponse with send, which is the new
// socket's own writer during a resume (connect, before Run has swapped it in — so
// nothing else can be writing to it) and s.Send afterwards. Anything that still
// cannot be written stays queued for the next attempt until the replay window
// expires. One flush at a time, so a response is never written twice or out of order.
func (s *Session) flushToolResponses(send func(any) error) {
	s.mu.Lock()
	if s.flushingTools || len(s.pendingTools) == 0 {
		s.mu.Unlock()
		return
	}
	s.flushingTools = true
	queue := s.pendingTools
	s.pendingTools = nil
	s.mu.Unlock()

	var keep []pendingTool
	for _, p := range queue {
		waited := time.Since(p.queued)
		if waited > toolResponseReplayWindow {
			logger.WarnCF("realtime", "gemini live: tool result dropped; no socket accepted it in time", map[string]any{
				"name": p.name, "waited_ms": waited.Milliseconds(),
			})
			continue
		}
		if err := send(p.payload); err != nil {
			keep = append(keep, p)
			continue
		}
		logger.InfoCF("realtime", "gemini live: tool result delivered on the resumed socket", map[string]any{
			"name": p.name, "waited_ms": waited.Milliseconds(),
		})
	}

	s.mu.Lock()
	s.pendingTools = append(keep, s.pendingTools...)
	pending := len(s.pendingTools) > 0
	s.flushingTools = false
	s.mu.Unlock()
	s.hasPendingTools.Store(pending)
}

// dropPendingToolResponses reports the results that never reached the model, so a
// stalled quiz has a log line naming it rather than only response_sent=false.
func (s *Session) dropPendingToolResponses() {
	s.mu.Lock()
	queue := s.pendingTools
	s.pendingTools = nil
	s.mu.Unlock()
	s.hasPendingTools.Store(false)
	for _, p := range queue {
		logger.WarnCF("realtime", "gemini live: tool result dropped; the session ended before a socket accepted it", map[string]any{
			"name": p.name, "waited_ms": time.Since(p.queued).Milliseconds(),
		})
	}
}

func (s *Session) onEnd(err error) {
	s.stopFlushTimer()           // Run has stopped accepting Go work, so a timer that already fired is a no-op
	s.flushUser()                // an utterance the model never answered is still part of the history
	s.dropPendingToolResponses() // no socket left to replay them on; say so rather than lose them silently
	if !s.Closing() {
		s.Emit(gptlive.Error{Err: errors.New("geminilive: connection lost: " + s.Scrub(fmt.Sprint(err))), Recoverable: false})
	}
	s.Emit(gptlive.Closed{Reason: "socket closed", VoiceSeconds: time.Since(s.started).Seconds()})
}
