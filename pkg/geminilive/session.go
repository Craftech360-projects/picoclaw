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
	s := &Session{cfg: cfg, started: time.Now()}
	ws, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	s.Conn = realtimeconn.New(ws)
	go s.Run(s.handleMessage, s.redial, s.onEnd)
	return s, nil
}

// connect dials, sends setup (carrying the resumption handle once there is one) and waits for setupComplete.
func (s *Session) connect(ctx context.Context) (*websocket.Conn, error) {
	// the key rides in the URL (Gemini's only API-key auth): never log this string
	escaped := url.QueryEscape(s.cfg.APIKey)
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

// AppendCommentary makes the model say something now. 3.1 models take realtime text;
// 2.5 models take a completed user turn.
func (s *Session) AppendCommentary(text string) {
	if strings.Contains(s.cfg.Model, "3.1") {
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
	UsageMetadata *struct {
		PromptTokenCount   int `json:"promptTokenCount"`
		ResponseTokenCount int `json:"responseTokenCount"`
		TotalTokenCount    int `json:"totalTokenCount"`
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
		return
	}
	if c := m.ServerContent; c != nil {
		if c.InputTranscription != nil {
			s.mu.Lock()
			s.userText += c.InputTranscription.Text
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
		if c.GenerationComplete || c.TurnComplete || c.Interrupted {
			s.stopFlushTimer()
			s.flushUser()
		}
		if c.Interrupted {
			s.Emit(gptlive.Interrupted{})
		}
		if c.Interrupted || c.TurnComplete {
			s.mu.Lock()
			s.turn++
			s.agentText = ""
			s.flushTimer = nil
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
	if u := m.UsageMetadata; u != nil {
		s.Emit(gptlive.BackendUsage{Model: s.cfg.Model, Input: u.PromptTokenCount, Output: u.ResponseTokenCount, Total: u.TotalTokenCount})
		s.Emit(gptlive.VoiceUsage{Seconds: time.Since(s.started).Seconds()})
	}
	if r := m.SessionResumptionUpdate; r != nil && r.Resumable && r.NewHandle != "" {
		s.mu.Lock()
		s.handle = r.NewHandle
		s.mu.Unlock()
	}
	if len(m.GoAway) > 0 {
		logger.InfoCF("realtime", "gemini live: goAway, the session resumes on a new connection", map[string]any{"go_away": string(m.GoAway)})
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
	output, isErr := realtimeconn.RunTool(s.cfg.Executor, call.Name, args)
	s.Emit(gptlive.FunctionResult{CallID: call.CallID, Name: call.Name, Output: output, IsError: isErr})
	response := map[string]any{"output": output}
	if isErr {
		response = map[string]any{"error": output}
	}
	_ = s.Send(map[string]any{"toolResponse": map[string]any{"functionResponses": []map[string]any{
		{"id": call.CallID, "name": call.Name, "response": response},
	}}})
}

func (s *Session) onEnd(err error) {
	s.stopFlushTimer() // Run has stopped accepting Go work, so a timer that already fired is a no-op
	s.flushUser()      // an utterance the model never answered is still part of the history
	if !s.Closing() {
		s.Emit(gptlive.Error{Err: fmt.Errorf("geminilive: connection lost: %v", err), Recoverable: false})
	}
	s.Emit(gptlive.Closed{Reason: "socket closed", VoiceSeconds: time.Since(s.started).Seconds()})
}
