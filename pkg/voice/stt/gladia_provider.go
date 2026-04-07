package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const gladiaLiveInitURL = "https://api.gladia.io/v2/live"

// gladiaProvider implements STT using Gladia Live STT v2 API.
// Flow (per docs):
// 1) POST /v2/live with audio configuration
// 2) Receive session id + websocket url
// 3) Stream audio chunks over websocket
type gladiaProvider struct {
	apiKey string
	model  string
}

// NewGladiaProvider creates a new Gladia provider.
func NewGladiaProvider(apiKey, model string) Provider {
	if model == "" {
		// Gladia live quickstart model example.
		model = "solaria-1"
	}
	return &gladiaProvider{apiKey: apiKey, model: model}
}

func (p *gladiaProvider) Name() string { return "gladia" }

func (p *gladiaProvider) WithConfig(apiKey, model string) Provider {
	return NewGladiaProvider(apiKey, model)
}

func (p *gladiaProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"},
		Models:               []string{"solaria-1"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *gladiaProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("GLADIA_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gladia: API key not configured")
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := opts.Channels
	if channels <= 0 {
		channels = 1
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}
	// Keep backward compatibility with old DB defaults used in this repo.
	if model == "" || strings.EqualFold(model, "gladia-2") {
		model = "solaria-1"
	}

	lang := strings.TrimSpace(opts.Language)
	lang = normalizeGladiaLang(lang)

	initBody := map[string]any{
		"encoding":    "wav/pcm",
		"sample_rate": sampleRate,
		"bit_depth":   16,
		"channels":    channels,
		"model":       model,
		"messages_config": map[string]any{
			"receive_partial_transcripts": opts.InterimResults,
		},
	}
	if lang != "" {
		initBody["language_config"] = map[string]any{
			"languages":      []string{lang},
			"code_switching": false,
		}
	}

	payload, err := json.Marshal(initBody)
	if err != nil {
		return nil, fmt.Errorf("gladia: marshal init request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", gladiaLiveInitURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gladia: build init request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-gladia-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gladia: init session request failed: %w", err)
	}

	var initResp struct {
		ID      string `json:"id"`
		URL     string `json:"url"`
		Message string `json:"message"`
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gladia: init session failed: %s - %s", resp.Status, string(bodyBytes))
	}
	if err := json.Unmarshal(bodyBytes, &initResp); err != nil {
		return nil, fmt.Errorf("gladia: decode init response: %w", err)
	}
	if initResp.URL == "" {
		return nil, fmt.Errorf("gladia: init response missing websocket URL")
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, initResp.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("gladia: websocket dial: %w", err)
	}

	stream := &gladiaLiveStream{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   lang,
	}

	logger.DebugCF("livekit", "Gladia live session started", map[string]any{
		"provider": "gladia",
		"id":       initResp.ID,
		"model":    model,
		"lang":     opts.Language,
	})

	go stream.readLoop()

	return stream, nil
}

type gladiaLiveStream struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *gladiaLiveStream) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}

	select {
	case <-s.closed:
		return fmt.Errorf("gladia stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *gladiaLiveStream) Results() <-chan TranscriptEvent {
	return s.resultChan
}

// Finalize is intentionally lightweight for Gladia Live STT.
// We keep the websocket session alive and let Gladia/VAD handle utterance boundaries.
func (s *gladiaLiveStream) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("gladia stream closed")
	default:
		return nil
	}
}

func (s *gladiaLiveStream) Close() error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		stopMsg := map[string]any{"type": "stop_recording"}
		if data, err := json.Marshal(stopMsg); err == nil {
			_ = s.conn.WriteMessage(websocket.TextMessage, data)
		}
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *gladiaLiveStream) readLoop() {
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
				logger.WarnCF("livekit", "Gladia read error", map[string]any{
					"provider": "gladia",
					"error":    err.Error(),
				})
			}
			return
		}

		var msg struct {
			Type    string `json:"type"`
			Message string `json:"message,omitempty"`
			Error   string `json:"error,omitempty"`
			Data    struct {
				ID        string `json:"id"`
				IsFinal   bool   `json:"is_final"`
				Text      string `json:"text,omitempty"`
				Utterance struct {
					Text string `json:"text"`
				} `json:"utterance"`
			} `json:"data"`
		}

		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "transcript":
			text := strings.TrimSpace(msg.Data.Utterance.Text)
			if text == "" {
				text = strings.TrimSpace(msg.Data.Text)
			}
			if text == "" {
				continue
			}

			evt := TranscriptEvent{
				Text:     text,
				IsFinal:  msg.Data.IsFinal,
				Language: s.language,
			}
			if !s.speaking {
				evt.SpeechStart = true
				s.speaking = true
			}
			if msg.Data.IsFinal {
				evt.SpeechEnd = true
				s.speaking = false
			}

			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "error":
			logger.ErrorCF("livekit", "Gladia live error", map[string]any{
				"provider": "gladia",
				"raw":      string(data),
				"error":    msg.Error,
			})
			return
		}
	}
}

func normalizeGladiaLang(lang string) string {
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
		// Accept ISO codes directly (e.g., en, hi, en-us).
		if len(lang) == 2 || len(lang) == 5 {
			return lang
		}
		return ""
	}
}
