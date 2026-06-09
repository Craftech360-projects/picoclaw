package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const sonioxRealtimeURL = "wss://stt-rt.soniox.com/transcribe-websocket"

// sonioxProvider implements STT using Soniox real-time WebSocket API.
type sonioxProvider struct {
	apiKey string
	model  string
}

// NewSonioxProvider creates a new Soniox provider.
func NewSonioxProvider(apiKey, model string) Provider {
	if model == "" {
		// Current Soniox realtime model family.
		model = "stt-rt-v4"
	}
	return &sonioxProvider{apiKey: apiKey, model: model}
}

func (p *sonioxProvider) Name() string { return "soniox" }

func (p *sonioxProvider) WithConfig(apiKey, model string) Provider {
	return NewSonioxProvider(apiKey, model)
}

func (p *sonioxProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"},
		Models:               []string{"stt-rt-v4", "stt-rt-preview"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *sonioxProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SONIOX_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("soniox: API key not configured")
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := opts.Channels
	if channels <= 0 {
		channels = 1
	}

	model := normalizeSonioxModel(p.model)
	if opts.Model != "" {
		model = normalizeSonioxModel(opts.Model)
	}

	langHint := normalizeSonioxLang(opts.Language)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, sonioxRealtimeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("soniox: websocket dial: %w", err)
	}

	startReq := map[string]any{
		"api_key":                   apiKey,
		"model":                     model,
		"audio_format":              "s16le",
		"sample_rate":               sampleRate,
		"num_channels":              channels,
		"enable_endpoint_detection": true,
		"max_endpoint_delay_ms":     800,
	}
	if langHint != "" {
		startReq["language_hints"] = []string{langHint}
	}

	startData, err := json.Marshal(startReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("soniox: marshal start request: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, startData); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("soniox: send start request: %w", err)
	}

	stream := &sonioxRealtimeStream{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   langHint,
	}

	logger.DebugCF("livekit", "Soniox realtime session started", map[string]any{
		"provider":    "soniox",
		"model":       model,
		"sample_rate": sampleRate,
		"channels":    channels,
		"lang_hint":   langHint,
	})

	go stream.readLoop()
	return stream, nil
}

type sonioxRealtimeStream struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *sonioxRealtimeStream) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("soniox stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *sonioxRealtimeStream) Results() <-chan TranscriptEvent {
	return s.resultChan
}

// Finalize current utterance without closing the session.
func (s *sonioxRealtimeStream) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("soniox stream closed")
	default:
	}

	// Soniox manual finalization:
	// https://soniox.com/docs/stt/core-concepts/manual-finalization
	// Sending this forces pending tokens to become final and emits <fin>.
	ctrl := map[string]any{
		"type":                "finalize",
		"trailing_silence_ms": 300,
	}
	data, err := json.Marshal(ctrl)
	if err != nil {
		return fmt.Errorf("soniox: marshal finalize control: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("soniox: send finalize control: %w", err)
	}

	logger.DebugCF("livekit", "Soniox finalize sent", map[string]any{
		"provider": "soniox",
	})
	return nil
}

func (s *sonioxRealtimeStream) Close() error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		// End stream by sending empty binary frame (per Soniox docs).
		_ = s.conn.WriteMessage(websocket.BinaryMessage, []byte{})
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *sonioxRealtimeStream) readLoop() {
	defer func() {
		_ = s.Close()
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
			default:
				logger.WarnCF("livekit", "Soniox read error", map[string]any{
					"provider": "soniox",
					"error":    err.Error(),
				})
			}
			return
		}

		var msg struct {
			Tokens []struct {
				Text       string  `json:"text"`
				IsFinal    bool    `json:"is_final"`
				Language   string  `json:"language"`
				Speaker    string  `json:"speaker"`
				Confidence float64 `json:"confidence"`
			} `json:"tokens"`
			Finished     bool   `json:"finished"`
			ErrorCode    int    `json:"error_code"`
			ErrorMessage string `json:"error_message"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		if msg.ErrorCode != 0 {
			logger.ErrorCF("livekit", "Soniox API error", map[string]any{
				"provider":      "soniox",
				"error_code":    msg.ErrorCode,
				"error_message": msg.ErrorMessage,
				"raw":           string(data),
			})
			return
		}

		if msg.Finished {
			logger.DebugCF("livekit", "Soniox stream finished", map[string]any{
				"provider": "soniox",
			})
			return
		}

		finalText, hasFinal, hasFinMarker, conf := collectSonioxText(msg.Tokens)
		if finalText == "" && !hasFinMarker {
			if len(msg.Tokens) > 0 {
				logger.DebugCF("livekit", "Soniox token update (no final text yet)", map[string]any{
					"provider":       "soniox",
					"token_count":    len(msg.Tokens),
					"has_fin_marker": hasFinMarker,
				})
			}
			continue
		}

		evt := TranscriptEvent{
			Text:       finalText,
			IsFinal:    hasFinal,
			Language:   s.language,
			Confidence: conf,
		}
		if finalText != "" && !s.speaking {
			evt.SpeechStart = true
			s.speaking = true
		}
		if hasFinMarker {
			evt.SpeechEnd = true
			s.speaking = false
		}

		select {
		case s.resultChan <- evt:
			logger.DebugCF("livekit", "Soniox transcript event emitted", map[string]any{
				"provider":    "soniox",
				"text":        "[redacted]",
				"text_len":    len(finalText),
				"is_final":    hasFinal,
				"speech_end":  hasFinMarker,
				"confidence":  conf,
				"token_count": len(msg.Tokens),
			})
		case <-s.closed:
			return
		}
	}
}

func collectSonioxText(tokens []struct {
	Text       string  `json:"text"`
	IsFinal    bool    `json:"is_final"`
	Language   string  `json:"language"`
	Speaker    string  `json:"speaker"`
	Confidence float64 `json:"confidence"`
}) (text string, hasFinal bool, hasFinMarker bool, avgConfidence float64) {
	var b strings.Builder
	var confSum float64
	var confCount int

	for _, tok := range tokens {
		t := strings.TrimSpace(tok.Text)
		if t == "" {
			continue
		}
		if t == "<fin>" || t == "<end>" {
			hasFinMarker = true
			continue
		}
		if !tok.IsFinal {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(t)
		hasFinal = true
		if tok.Confidence > 0 {
			confSum += tok.Confidence
			confCount++
		}
	}

	if confCount > 0 {
		avgConfidence = confSum / float64(confCount)
	}
	return b.String(), hasFinal, hasFinMarker, avgConfidence
}

func normalizeSonioxModel(model string) string {
	m := strings.TrimSpace(strings.ToLower(model))
	switch m {
	case "", "stt-rt-v4":
		return "stt-rt-v4"
	case "stt-rt-preview":
		return "stt-rt-preview"
	// Backward compatibility with legacy values in DB.
	case "standard_v2", "premium_v1_short", "standard", "enhanced":
		return "stt-rt-v4"
	default:
		return "stt-rt-v4"
	}
}

func normalizeSonioxLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "", "auto":
		return ""
	case "english":
		return "en"
	case "hindi":
		return "hi"
	case "spanish":
		return "es"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "italian":
		return "it"
	case "portuguese":
		return "pt"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "chinese", "mandarin":
		return "zh"
	default:
		if len(lang) == 2 || len(lang) == 5 {
			return lang
		}
		return ""
	}
}
