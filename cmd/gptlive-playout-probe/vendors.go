package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/gptlive"
)

// voiceSession is what the probe needs from a realtime voice vendor: mic in, model audio
// out (PCM16 mono at outRate), a barge-in signal, a way to make the model speak first.
type voiceSession interface {
	PushAudio(pcm []byte)         // PCM16 mono at the vendor's input rate
	Audio() <-chan []byte         // PCM16 mono at outRate
	Interrupted() <-chan struct{} // the user started talking over the model; nil if the vendor never says
	Greet(text string)
	Close()
}

const outRate = 24000 // all three vendors speak 24kHz PCM16

type vendorSpec struct {
	inRate       int
	defaultVoice string
	defaultModel string
	envKey       string
}

var vendors = map[string]vendorSpec{
	"openai": {24000, "marin", gptlive.DefaultModel, "OPENAI_API_KEY"},
	"xai":    {24000, "ara", "grok-voice-think-fast-1.0", "XAI_API_KEY"},
	"google": {16000, "Puck", "gemini-2.5-flash-native-audio-preview-12-2025", "GOOGLE_API_KEY"}, // Live API input is 16kHz only
}

type dialOptions struct {
	key, model, voice, instructions string
	silence                         time.Duration // end-of-turn silence for server VAD
	baseURL                         string        // tests only: a fake vendor socket
}

func or(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func dialVendor(ctx context.Context, vendor string, o dialOptions) (voiceSession, error) {
	switch vendor {
	case "xai":
		return dialXAI(ctx, o)
	case "google":
		return dialGemini(ctx, o)
	}
	sess, err := gptlive.Dial(ctx, gptlive.Config{APIKey: o.key, Model: o.model, Voice: o.voice, SampleRate: outRate, Instructions: o.instructions})
	if err != nil {
		return nil, err
	}
	go func() {
		for ev := range sess.Events() {
			if e, ok := ev.(gptlive.Error); ok {
				log.Printf("gptlive error (recoverable=%v): %v", e.Recoverable, e.Err)
			}
		}
	}()
	return gptliveSession{sess}, nil
}

// gptliveSession adapts pkg/gptlive. GPT-Live owns turn-taking itself, so there is no barge-in signal.
type gptliveSession struct{ *gptlive.Session }

func (g gptliveSession) Interrupted() <-chan struct{} { return nil }
func (g gptliveSession) Greet(text string)            { g.AppendCommentary(text) }
func (g gptliveSession) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.Session.Close(ctx)
}

// wsSession is the shared websocket plumbing for xAI and Gemini: serialized writes, and a
// read loop that never blocks on a slow consumer (the lesson from the GPT-Live jitter hunt).
type wsSession struct {
	conn        *websocket.Conn
	writeMu     sync.Mutex
	audio       chan []byte
	interrupted chan struct{}
	closeOnce   sync.Once
}

func newWSSession(conn *websocket.Conn) *wsSession {
	return &wsSession{conn: conn, audio: make(chan []byte, 1024), interrupted: make(chan struct{}, 1)}
}

func (s *wsSession) Audio() <-chan []byte         { return s.audio }
func (s *wsSession) Interrupted() <-chan struct{} { return s.interrupted }
func (s *wsSession) Close()                       { s.closeOnce.Do(func() { _ = s.conn.Close() }) }

func (s *wsSession) send(v any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.WriteJSON(v); err != nil {
		log.Printf("send: %v", err)
	}
}

func (s *wsSession) emitAudio(b64 string) {
	pcm, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(pcm) == 0 {
		return
	}
	select {
	case s.audio <- pcm:
	default: // ponytail: ~20s of backlog; drop rather than stall the socket read
		log.Printf("model audio backlog full, dropping %d bytes", len(pcm))
	}
}

func (s *wsSession) interrupt() {
	select {
	case s.interrupted <- struct{}{}:
	default:
	}
}

// readLoop hands every text or binary frame to handle until the socket closes, then closes Audio().
func (s *wsSession) readLoop(handle func([]byte)) {
	defer close(s.audio)
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			log.Printf("vendor socket closed: %v", err)
			return
		}
		handle(data)
	}
}

// --- xAI Grok Voice: OpenAI Realtime-style events (docs.x.ai/docs/guides/voice/agent) ---

type xaiSession struct{ *wsSession }

func dialXAI(ctx context.Context, o dialOptions) (voiceSession, error) {
	url := or(o.baseURL, "wss://api.x.ai/v1/realtime") + "?model=" + neturl.QueryEscape(o.model)
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, url, http.Header{"Authorization": {"Bearer " + o.key}})
	if err != nil {
		return nil, handshakeErr("xai", o.key, resp, err)
	}
	s := xaiSession{newWSSession(conn)}
	pcm := map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}
	s.send(map[string]any{"type": "session.update", "session": map[string]any{
		"voice":          o.voice,
		"instructions":   o.instructions,
		"turn_detection": map[string]any{"type": "server_vad", "silence_duration_ms": o.silence.Milliseconds()},
		"audio":          map[string]any{"input": pcm, "output": pcm},
	}})
	go s.readLoop(func(raw []byte) {
		var ev struct {
			Type  string          `json:"type"`
			Delta string          `json:"delta"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		switch ev.Type {
		case "response.output_audio.delta", "response.audio.delta":
			s.emitAudio(ev.Delta)
		case "input_audio_buffer.speech_started":
			s.interrupt()
		case "error":
			log.Printf("xai error: %s", ev.Error)
		}
	})
	return s, nil
}

func (s xaiSession) PushAudio(pcm []byte) {
	s.send(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)})
}

func (s xaiSession) Greet(text string) {
	s.send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}},
	}})
	s.send(map[string]any{"type": "response.create"})
}

// --- Google Gemini Live: BidiGenerateContent (ai.google.dev/api/live) ---

type geminiSession struct {
	*wsSession
	model string
}

func dialGemini(ctx context.Context, o dialOptions) (voiceSession, error) {
	// the key rides in the URL (Gemini's only API-key auth): never log this string
	url := or(o.baseURL, "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent") +
		"?key=" + neturl.QueryEscape(o.key)
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, handshakeErr("google", o.key, resp, err)
	}
	s := geminiSession{newWSSession(conn), o.model}
	s.send(map[string]any{"setup": map[string]any{
		"model": "models/" + o.model,
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": o.voice}}},
		},
		"systemInstruction":   map[string]any{"parts": []map[string]any{{"text": o.instructions}}},
		"realtimeInputConfig": map[string]any{"automaticActivityDetection": map[string]any{"silenceDurationMs": o.silence.Milliseconds()}},
	}})

	// the server must answer setupComplete before it accepts audio or text
	ready := make(chan struct{})
	var readyOnce sync.Once
	go s.readLoop(func(raw []byte) {
		var msg struct {
			SetupComplete *struct{} `json:"setupComplete"`
			ServerContent *struct {
				ModelTurn *struct {
					Parts []struct {
						InlineData *struct {
							Data string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"modelTurn"`
				Interrupted bool `json:"interrupted"`
			} `json:"serverContent"`
			GoAway json.RawMessage `json:"goAway"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			return
		}
		if msg.SetupComplete != nil {
			readyOnce.Do(func() { close(ready) })
		}
		if c := msg.ServerContent; c != nil {
			if c.Interrupted {
				s.interrupt()
			}
			if c.ModelTurn != nil {
				for _, p := range c.ModelTurn.Parts {
					if p.InlineData != nil {
						s.emitAudio(p.InlineData.Data)
					}
				}
			}
		}
		if len(msg.GoAway) > 0 {
			log.Printf("gemini goAway: %s", msg.GoAway)
		}
		if len(msg.Error) > 0 {
			log.Printf("gemini error: %s", msg.Error)
		}
	})
	select {
	case <-ready:
		return s, nil
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	s.Close()
	return nil, errors.New("gemini: no setupComplete within 10s (check model name and key)")
}

func (s geminiSession) PushAudio(pcm []byte) {
	s.send(map[string]any{"realtimeInput": map[string]any{
		"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(pcm), "mimeType": "audio/pcm;rate=16000"},
	}})
}

// Greet: 3.1 accepts clientContent only for seeding history, so it takes realtime text;
// 2.5 gets a normal user turn.
func (s geminiSession) Greet(text string) {
	if strings.Contains(s.model, "3.1") {
		s.send(map[string]any{"realtimeInput": map[string]any{"text": text}})
		return
	}
	s.send(map[string]any{"clientContent": map[string]any{
		"turns": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": text}}}}, "turnComplete": true,
	}})
}

// handshakeErr reports a failed dial without ever echoing the key (Gemini's rides in the URL).
func handshakeErr(vendor, key string, resp *http.Response, err error) error {
	if resp != nil {
		return fmt.Errorf("%s: websocket handshake refused: HTTP %d", vendor, resp.StatusCode)
	}
	return fmt.Errorf("%s: dial: %s", vendor, strings.ReplaceAll(err.Error(), key, "***"))
}
