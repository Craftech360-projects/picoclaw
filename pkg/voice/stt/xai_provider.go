package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const xaiStreamingURL = "wss://api.x.ai/v1/stt"

// xaiProvider implements STT using xAI's realtime WebSocket API.
type xaiProvider struct {
	apiKey string
	model  string
}

// NewXAIProvider creates a new xAI STT provider.
func NewXAIProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "stt"
	}
	return &xaiProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *xaiProvider) Name() string { return "xai" }

func (p *xaiProvider) WithConfig(apiKey, model string) Provider {
	return NewXAIProvider(apiKey, model)
}

func (p *xaiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{
			"ar", "cs", "da", "de", "en", "es", "fa", "fil", "fr", "hi", "id", "it", "ja", "ko", "mk",
			"ms", "nl", "pl", "pt", "ro", "ru", "sv", "th", "tr", "vi",
		},
		Models:               []string{"stt"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *xaiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("xai: API key not configured")
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := opts.Channels
	if channels <= 0 {
		channels = 1
	}

	wsURL, err := xaiStreamURL(opts, sampleRate, channels)
	if err != nil {
		return nil, err
	}

	conn, err := dialXAIStream(ctx, wsURL, apiKey)
	if err != nil {
		return nil, err
	}

	stream := &xaiStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		ctx:        ctx,
		apiKey:     apiKey,
		wsURL:      wsURL,
	}

	logger.DebugCF("livekit", "xAI websocket opened", map[string]any{
		"provider":        "xai",
		"model":           p.model,
		"sample_rate":     sampleRate,
		"channels":        channels,
		"language":        normalizeXAILanguage(opts.Language),
		"interim_results": opts.InterimResults,
		"endpointing_ms":  opts.EndpointingMS,
	})

	go stream.readLoop()
	return stream, nil
}

type xaiStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	ctx        context.Context
	apiKey     string
	wsURL      string
	speaking   bool
	finalizing bool
	reconnect  bool
	pendingPCM []byte
	finalParts []string
}

func (s *xaiStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("xai stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalizing || s.reconnect {
		s.pendingPCM = append(s.pendingPCM, pcm...)
		return nil
	}
	if err := s.conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
		return fmt.Errorf("xai send audio: %w", err)
	}
	return nil
}

func (s *xaiStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *xaiStreamAdapter) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("xai stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalizing {
		return nil
	}
	if err := s.conn.WriteJSON(map[string]string{"type": "audio.done"}); err != nil {
		return fmt.Errorf("xai finalize audio: %w", err)
	}
	s.finalizing = true
	return nil
}

func (s *xaiStreamAdapter) Close() error {
	return s.close(true)
}

func (s *xaiStreamAdapter) close(sendClose bool) error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		if sendClose {
			_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		}
		retErr = s.conn.Close()
		s.mu.Unlock()
	})
	return retErr
}

func (s *xaiStreamAdapter) readLoop() {
	defer func() {
		if s.isClosed() {
			_ = s.close(false)
		}
		close(s.resultChan)
	}()

	for {
		select {
		case <-s.closed:
			return
		default:
		}

		_, data, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				logger.WarnCF("livekit", "xAI read error", map[string]any{
					"provider": "xai",
					"error":    err.Error(),
				})
			}
			if err := s.reconnectStream(); err != nil {
				select {
				case <-s.closed:
				default:
					logger.ErrorCF("livekit", "xAI reconnect failed", map[string]any{
						"provider": "xai",
						"error":    err.Error(),
					})
				}
				return
			}
			continue
		}

		msg, err := parseXAIEvent(data)
		if err != nil {
			logger.WarnCF("livekit", "xAI event decode failed", map[string]any{
				"provider": "xai",
				"error":    err.Error(),
			})
			continue
		}

		switch msg.Type {
		case "transcript.created":
			continue

		case "transcript.partial":
			if msg.IsFinal {
				s.recordFinalPart(msg.Text)
			}
			evt := s.transcriptEvent(msg, msg.IsFinal, msg.SpeechFinal)
			if evt.Text == "" && !evt.SpeechStart && !evt.SpeechEnd {
				continue
			}
			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "transcript.done":
			if strings.TrimSpace(msg.Text) == "" {
				msg.Text = s.stitchedFinalText()
			}
			evt := s.transcriptEvent(msg, true, true)
			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}
			s.clearFinalParts()
			if err := s.flushPendingAfterDone(); err != nil {
				logger.WarnCF("livekit", "xAI buffered audio flush failed", map[string]any{
					"provider": "xai",
					"error":    err.Error(),
				})
				return
			}

		case "error":
			errText := strings.TrimSpace(msg.Message)
			if errText == "" {
				errText = strings.TrimSpace(msg.Error)
			}
			logger.ErrorCF("livekit", "xAI websocket error", map[string]any{
				"provider": "xai",
				"error":    errText,
			})
		}
	}
}

func (s *xaiStreamAdapter) recordFinalPart(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(s.finalParts) > 0 && s.finalParts[len(s.finalParts)-1] == text {
		return
	}
	s.finalParts = append(s.finalParts, text)
}

func (s *xaiStreamAdapter) stitchedFinalText() string {
	return strings.TrimSpace(strings.Join(s.finalParts, " "))
}

func (s *xaiStreamAdapter) clearFinalParts() {
	s.finalParts = nil
}

func (s *xaiStreamAdapter) reconnectStream() error {
	s.mu.Lock()
	if s.isClosedLocked() {
		s.mu.Unlock()
		return fmt.Errorf("xai stream closed")
	}
	oldConn := s.conn
	s.reconnect = true
	s.finalizing = false
	s.mu.Unlock()

	_ = oldConn.Close()

	backoff := 100 * time.Millisecond
	for {
		select {
		case <-s.closed:
			return fmt.Errorf("xai stream closed")
		case <-s.ctx.Done():
			return s.ctx.Err()
		default:
		}

		conn, err := dialXAIStream(s.ctx, s.wsURL, s.apiKey)
		if err != nil {
			logger.WarnCF("livekit", "xAI reconnect attempt failed", map[string]any{
				"provider": "xai",
				"error":    err.Error(),
				"backoff":  backoff.String(),
			})
			time.Sleep(backoff)
			if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}

		s.mu.Lock()
		if s.isClosedLocked() {
			s.mu.Unlock()
			_ = conn.Close()
			return fmt.Errorf("xai stream closed")
		}
		s.conn = conn
		s.reconnect = false
		pendingPCM := s.pendingPCM
		s.pendingPCM = nil
		if len(pendingPCM) > 0 {
			err = s.conn.WriteMessage(websocket.BinaryMessage, pendingPCM)
		}
		s.mu.Unlock()

		if err != nil {
			_ = conn.Close()
			continue
		}

		logger.InfoCF("livekit", "xAI websocket reconnected", map[string]any{
			"provider": "xai",
		})
		return nil
	}
}

func (s *xaiStreamAdapter) flushPendingAfterDone() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.finalizing = false
	if len(s.pendingPCM) == 0 {
		return nil
	}
	pcm := s.pendingPCM
	s.pendingPCM = nil
	if err := s.conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
		return fmt.Errorf("xai flush buffered audio: %w", err)
	}
	return nil
}

func (s *xaiStreamAdapter) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *xaiStreamAdapter) isClosedLocked() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *xaiStreamAdapter) transcriptEvent(msg xaiEvent, isFinal, speechFinal bool) TranscriptEvent {
	text := strings.TrimSpace(msg.Text)
	evt := TranscriptEvent{
		Text:      text,
		IsFinal:   isFinal,
		SpeechEnd: speechFinal,
		Language:  strings.TrimSpace(msg.Language),
		Duration:  msg.Duration,
	}
	if text != "" && !s.speaking {
		evt.SpeechStart = true
		s.speaking = true
	}
	if speechFinal {
		s.speaking = false
	}
	return evt
}

type xaiEvent struct {
	Type        string  `json:"type"`
	Text        string  `json:"text"`
	IsFinal     bool    `json:"is_final"`
	SpeechFinal bool    `json:"speech_final"`
	Start       float64 `json:"start"`
	Duration    float64 `json:"duration"`
	Language    string  `json:"language"`
	Message     string  `json:"message"`
	Error       string  `json:"error"`
}

func parseXAIEvent(data []byte) (xaiEvent, error) {
	var msg xaiEvent
	if err := json.Unmarshal(data, &msg); err != nil {
		return xaiEvent{}, err
	}
	msg.Type = strings.TrimSpace(msg.Type)
	return msg, nil
}

func dialXAIStream(ctx context.Context, wsURL, apiKey string) (*websocket.Conn, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(bodyBytes) > 0 {
				return nil, fmt.Errorf("xai websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(bodyBytes)))
			}
			return nil, fmt.Errorf("xai websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("xai websocket dial failed: %w", err)
	}

	if err := waitForXAIReady(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func waitForXAIReady(conn *websocket.Conn) error {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("xai wait for ready: %w", err)
	}
	msg, err := parseXAIEvent(data)
	if err != nil {
		return fmt.Errorf("xai decode ready event: %w", err)
	}
	switch msg.Type {
	case "transcript.created":
		return nil
	case "error":
		errText := strings.TrimSpace(msg.Message)
		if errText == "" {
			errText = strings.TrimSpace(msg.Error)
		}
		if errText == "" {
			errText = string(data)
		}
		return fmt.Errorf("xai websocket error before ready: %s", errText)
	default:
		return fmt.Errorf("xai expected transcript.created, got %q", msg.Type)
	}
}

func xaiStreamURL(opts StreamOptions, sampleRate, channels int) (string, error) {
	u, err := url.Parse(xaiStreamingBaseURL())
	if err != nil {
		return "", fmt.Errorf("xai streaming URL: %w", err)
	}

	q := u.Query()
	q.Set("sample_rate", strconv.Itoa(sampleRate))
	q.Set("encoding", "pcm")
	q.Set("interim_results", strconv.FormatBool(opts.InterimResults))
	if opts.EndpointingMS > 0 {
		q.Set("endpointing", strconv.Itoa(opts.EndpointingMS))
	}
	if language := normalizeXAILanguage(opts.Language); language != "" {
		q.Set("language", language)
	}
	if channels > 1 {
		q.Set("multichannel", "true")
	}
	q.Set("channels", strconv.Itoa(channels))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func xaiStreamingBaseURL() string {
	if override := strings.TrimSpace(os.Getenv("XAI_STT_STREAMING_URL")); override != "" {
		return override
	}
	return xaiStreamingURL
}

func normalizeXAILanguage(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ""
	}
	lower := strings.ToLower(lang)
	switch lower {
	case "auto", "multi", "multilingual", "unknown":
		return ""
	case "arabic":
		return "ar"
	case "czech":
		return "cs"
	case "danish":
		return "da"
	case "dutch":
		return "nl"
	case "english":
		return "en"
	case "filipino", "tagalog":
		return "fil"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "hindi":
		return "hi"
	case "indonesian":
		return "id"
	case "italian":
		return "it"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "macedonian":
		return "mk"
	case "malay":
		return "ms"
	case "persian", "farsi":
		return "fa"
	case "polish":
		return "pl"
	case "portuguese":
		return "pt"
	case "romanian":
		return "ro"
	case "russian":
		return "ru"
	case "spanish":
		return "es"
	case "swedish":
		return "sv"
	case "thai":
		return "th"
	case "turkish":
		return "tr"
	case "vietnamese":
		return "vi"
	}
	if len(lower) == 2 || lower == "fil" {
		return lower
	}
	if len(lower) == 5 && lower[2] == '-' {
		return lower[:2] + "-" + strings.ToUpper(lower[3:])
	}
	return ""
}
