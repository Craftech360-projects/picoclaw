package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// The Gemini Live API bidirectional endpoint. Auth is a `key` query parameter;
// this endpoint does not accept an Authorization header.
const geminiSTTWebsocketURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

const geminiDefaultModel = "gemini-3.5-transcribe-live"

// geminiProvider implements streaming STT on gemini-3.5-transcribe-live.
type geminiProvider struct {
	apiKey string
	model  string
}

func NewGeminiProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = geminiDefaultModel
	}
	return &geminiProvider{apiKey: apiKey, model: model}
}

func (p *geminiProvider) Name() string { return "gemini" }

func (p *geminiProvider) WithConfig(apiKey, model string) Provider {
	return NewGeminiProvider(apiKey, model)
}

func (p *geminiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "hi", "bn", "ta", "te", "mr", "gu", "kn", "ml", "pa", "auto"},
		Models:               []string{geminiDefaultModel},
		SupportsStreaming:    true,
		SupportsDiarization:  false, // live sessions carry no speaker attribution
		SupportsMultilingual: true,
	}
}

func (p *geminiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = geminiDefaultModel
	}

	q := url.Values{}
	q.Set("key", apiKey)
	connURL := geminiSTTURL() + "?" + q.Encode()

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, connURL, http.Header{})
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(body) > 0 {
				return nil, fmt.Errorf("gemini websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(body)))
			}
			return nil, fmt.Errorf("gemini websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("gemini websocket dial failed: %w", err)
	}

	languages := normalizeGeminiLanguages(opts.Language)
	setup := map[string]any{
		"setup": map[string]any{
			"model": "models/" + model,
			"generationConfig": map[string]any{
				"responseModalities": []string{"TEXT"},
			},
			"inputAudioTranscription": map[string]any{
				// An empty slice is the documented auto-detect value.
				"languageCodes": languages,
				"mode":          "SMART",
			},
			"realtimeInputConfig": map[string]any{
				// Manual activity detection: the device's Manual Talk tap is the
				// sole Turn Boundary authority (ADR 0007). Server VAD stays off.
				"automaticActivityDetection": map[string]any{"disabled": true},
			},
		},
	}
	payload, err := json.Marshal(setup)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: marshal setup: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: write setup: %w", err)
	}

	// Block on setupComplete so a rejected model or bad key surfaces here, at
	// OpenStream, rather than as a silent socket that never transcribes.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: awaiting setupComplete: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if !strings.Contains(string(raw), "setupComplete") {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: setup rejected: %s", truncateForLog(string(raw), 512))
	}

	stream := &geminiStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   strings.Join(languages, ","),
	}

	logger.DebugCF("livekit", "Gemini STT websocket opened", map[string]any{
		"provider":  "gemini",
		"model":     model,
		"languages": languages,
	})

	go stream.readLoop()
	return stream, nil
}

type geminiStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string

	writeMu   sync.Mutex
	closeOnce sync.Once

	turnMu        sync.Mutex
	emptyHandler  func()
	sawAudio      bool
	cancelled     bool
	gotFinal      bool
	activityEnded bool
	turnGen       uint64
}

func (s *geminiStreamAdapter) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.closed:
		return fmt.Errorf("gemini: stream closed")
	default:
	}
	return s.conn.WriteMessage(websocket.TextMessage, payload)
}

func (s *geminiStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	s.turnMu.Lock()
	s.sawAudio = true
	s.turnMu.Unlock()

	return s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{
			"audio": map[string]any{
				"data":     base64.StdEncoding.EncodeToString(pcm),
				"mimeType": "audio/pcm;rate=16000",
			},
		},
	})
}

func (s *geminiStreamAdapter) Results() <-chan TranscriptEvent { return s.resultChan }

// ResetBuffer is a fresh press: open a new activity window and drop any
// suppression left over from the previous turn.
func (s *geminiStreamAdapter) ResetBuffer() {
	s.turnMu.Lock()
	s.sawAudio = false
	s.cancelled = false
	s.gotFinal = false
	s.activityEnded = false
	s.turnGen++
	s.turnMu.Unlock()

	if err := s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{"activityStart": map[string]any{}},
	}); err != nil {
		logger.DebugCF("livekit", "Gemini activityStart failed", map[string]any{
			"provider": "gemini",
			"error":    err.Error(),
		})
	}
}

// CancelTurn is deliberate silence. The audio already reached Google, so the
// final still arrives; suppress it on the way out and skip the empty-tap reply.
//
// Deliberately sends nothing: room_session.go:554-562 calls this twice per
// cancel and then drives Finalize, and Finalize owns the single activityEnd.
// Idempotent by construction.
func (s *geminiStreamAdapter) CancelTurn() {
	s.turnMu.Lock()
	s.cancelled = true
	s.turnMu.Unlock()
}

// SetEmptyResultHandler registers the "I didn't hear you" callback.
func (s *geminiStreamAdapter) SetEmptyResultHandler(fn func()) {
	s.turnMu.Lock()
	s.emptyHandler = fn
	s.turnMu.Unlock()
}

// geminiEmptyGrace is how long Finalize waits for a final before calling the
// tap empty.
// ponytail: one fixed window, not adaptive. If real devices show finals landing
// later than this on slow networks, widen GEMINI_STT_EMPTY_GRACE rather than
// building latency tracking.
func geminiEmptyGrace() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GEMINI_STT_EMPTY_GRACE")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Second
}

// Finalize is End Turn: close the activity window, then decide whether the tap
// produced anything. A tap that carried audio but drew no final gets the
// empty-result reply.
//
// The activityEnd is one-shot per turn generation. Both PTT paths can drive
// more than one Finalize for a single turn — the cancel path calls CancelTurn
// twice and then finalizes, and a 25s cap can race a speech_end — and the Live
// API has no open activity window to close the second time.
func (s *geminiStreamAdapter) Finalize() error {
	s.turnMu.Lock()
	gen := s.turnGen
	sawAudio := s.sawAudio
	cancelled := s.cancelled
	alreadyEnded := s.activityEnded
	s.activityEnded = true
	s.turnMu.Unlock()

	if alreadyEnded {
		return nil
	}

	err := s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{"activityEnd": map[string]any{}},
	})

	if sawAudio && !cancelled {
		go s.announceIfEmpty(gen, geminiEmptyGrace())
	}
	return err
}

// announceIfEmpty fires the empty-result handler when the grace window closes
// with no final for this turn. Keyed on turnGen so a press landing during the
// wait cancels the announcement for the turn it superseded.
func (s *geminiStreamAdapter) announceIfEmpty(gen uint64, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.closed:
		return
	}

	s.turnMu.Lock()
	stale := s.turnGen != gen
	quiet := !s.gotFinal && !s.cancelled
	handler := s.emptyHandler
	s.turnMu.Unlock()

	if stale || !quiet || handler == nil {
		return
	}
	logger.InfoCF("livekit", "Gemini tap produced no transcript", map[string]any{
		"provider": "gemini",
		"grace":    grace.String(),
	})
	handler()
}

func (s *geminiStreamAdapter) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.writeMu.Lock()
		_ = s.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = s.conn.Close()
		s.writeMu.Unlock()
	})
	return nil
}

func (s *geminiStreamAdapter) readLoop() {
	defer close(s.resultChan)
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.closed:
			default:
				logger.DebugCF("livekit", "Gemini STT read loop ended", map[string]any{
					"provider": "gemini",
					"error":    err.Error(),
				})
			}
			return
		}
		evt, ok := s.parseMessage(data)
		if !ok {
			continue
		}

		s.turnMu.Lock()
		if evt.IsFinal {
			s.gotFinal = true
		}
		suppressed := s.cancelled
		s.turnMu.Unlock()
		if suppressed {
			continue
		}

		select {
		case s.resultChan <- evt:
		case <-s.closed:
			return
		}
	}
}

// parseMessage maps one Live API server frame to a TranscriptEvent. Finals
// arrive as serverContent.inputTranscription, interims as
// serverContent.interimInputTranscription.
func (s *geminiStreamAdapter) parseMessage(data []byte) (TranscriptEvent, bool) {
	var msg struct {
		ServerContent struct {
			InputTranscription struct {
				Text string `json:"text"`
			} `json:"inputTranscription"`
			InterimInputTranscription struct {
				Text string `json:"text"`
			} `json:"interimInputTranscription"`
		} `json:"serverContent"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return TranscriptEvent{}, false
	}

	if text := strings.TrimSpace(msg.ServerContent.InputTranscription.Text); text != "" {
		return TranscriptEvent{Text: text, IsFinal: true, Language: s.language}, true
	}
	if text := strings.TrimSpace(msg.ServerContent.InterimInputTranscription.Text); text != "" {
		return TranscriptEvent{Text: text, IsFinal: false, Language: s.language}, true
	}
	return TranscriptEvent{}, false
}

// geminiSTTURL allows tests to point the dialer at a local socket.
func geminiSTTURL() string {
	if override := strings.TrimSpace(os.Getenv("GEMINI_STT_WS_URL")); override != "" {
		return override
	}
	return geminiSTTWebsocketURL
}

// normalizeGeminiLanguages turns our language setting into BCP-47 codes. An
// empty slice is the documented auto-detect value, so "auto" and "" both map
// to no pinned language.
func normalizeGeminiLanguages(lang string) []string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || l == "auto" || l == "multi" {
		return []string{}
	}
	if strings.Contains(l, "-") {
		parts := strings.SplitN(l, "-", 2)
		return []string{parts[0] + "-" + strings.ToUpper(parts[1])}
	}
	defaults := map[string]string{
		"en": "en-US", "hi": "hi-IN", "bn": "bn-IN", "ta": "ta-IN",
		"te": "te-IN", "mr": "mr-IN", "gu": "gu-IN", "kn": "kn-IN",
		"ml": "ml-IN", "pa": "pa-IN",
	}
	if code, ok := defaults[l]; ok {
		return []string{code}
	}
	return []string{l}
}
