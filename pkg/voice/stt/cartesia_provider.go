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

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const cartesiaSTTWebsocketURL = "wss://api.cartesia.ai/stt/websocket"
const cartesiaSTTAPIVersion = "2026-03-01"

// cartesiaProvider implements STT using Cartesia streaming WebSocket API.
type cartesiaProvider struct {
	apiKey string
	model  string
}

// NewCartesiaProvider creates a new Cartesia provider.
func NewCartesiaProvider(apiKey, model string) Provider {
	if model == "" {
		model = "ink-whisper"
	}
	return &cartesiaProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *cartesiaProvider) Name() string { return "cartesia" }

func (p *cartesiaProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"},
		Models:               []string{"ink-whisper"},
		SupportsStreaming:    true,
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *cartesiaProvider) WithConfig(apiKey, model string) Provider {
	return NewCartesiaProvider(apiKey, model)
}

func (p *cartesiaProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("CARTESIA_API_KEY")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("cartesia: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = "ink-whisper"
	}

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	language := normalizeCartesiaLang(opts.Language)
	if language == "" {
		// Cartesia STT requires a language value; default to English.
		language = "en"
	}

	maxSilenceSecs := 0.8
	if opts.EndpointingMS > 0 {
		maxSilenceSecs = float64(opts.EndpointingMS) / 1000.0
	}

	q := url.Values{}
	q.Set("model", model)
	q.Set("language", language)
	q.Set("encoding", "pcm_s16le")
	q.Set("sample_rate", strconv.Itoa(sampleRate))
	q.Set("min_volume", "0.1")
	q.Set("max_silence_duration_secs", fmt.Sprintf("%.3f", maxSilenceSecs))
	// Some websocket deployments only validate api_key in query params.
	// Keep header auth too for compatibility.
	q.Set("api_key", apiKey)

	wsURL := cartesiaSTTWebsocketURL + "?" + q.Encode()
	header := http.Header{}
	header.Set("X-API-Key", apiKey)
	header.Set("Cartesia-Version", cartesiaSTTAPIVersion)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(bodyBytes) > 0 {
				return nil, fmt.Errorf("cartesia websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(bodyBytes)))
			}
			return nil, fmt.Errorf("cartesia websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("cartesia websocket dial failed: %w", err)
	}

	stream := &cartesiaStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   language,
	}

	logger.DebugCF("livekit", "Cartesia STT websocket opened", map[string]any{
		"provider":      "cartesia",
		"model":         model,
		"language":      language,
		"sample_rate":   sampleRate,
		"endpoint_secs": maxSilenceSecs,
	})

	go stream.readLoop()
	return stream, nil
}

type cartesiaStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *cartesiaStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("cartesia stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *cartesiaStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *cartesiaStreamAdapter) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("cartesia stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.WriteMessage(websocket.TextMessage, []byte("finalize")); err != nil {
		return fmt.Errorf("cartesia finalize: %w", err)
	}
	logger.DebugCF("livekit", "Cartesia finalize sent", map[string]any{"provider": "cartesia"})
	return nil
}

func (s *cartesiaStreamAdapter) Close() error {
	return s.close(true)
}

func (s *cartesiaStreamAdapter) close(sendDone bool) error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		if sendDone {
			_ = s.conn.WriteMessage(websocket.TextMessage, []byte("done"))
		}
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *cartesiaStreamAdapter) readLoop() {
	defer func() {
		_ = s.close(false)
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
				logger.WarnCF("livekit", "Cartesia STT read error", map[string]any{
					"provider": "cartesia",
					"error":    err.Error(),
				})
			}
			return
		}

		var msg struct {
			Type      string  `json:"type"`
			IsFinal   bool    `json:"is_final"`
			Text      string  `json:"text"`
			Duration  float64 `json:"duration"`
			Language  string  `json:"language"`
			Error     string  `json:"error"`
			RequestID string  `json:"request_id"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "transcript":
			lang := strings.TrimSpace(msg.Language)
			if lang == "" {
				lang = s.language
			}
			text := strings.TrimSpace(msg.Text)
			evt := TranscriptEvent{
				Text:     text,
				IsFinal:  msg.IsFinal,
				Language: lang,
				Duration: msg.Duration,
			}
			if text != "" && !s.speaking {
				evt.SpeechStart = true
				s.speaking = true
			}
			if msg.IsFinal {
				evt.SpeechEnd = true
				s.speaking = false
			}

			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "flush_done":
			evt := TranscriptEvent{
				IsFinal:   true,
				SpeechEnd: true,
				Language:  s.language,
			}
			s.speaking = false
			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "done":
			logger.DebugCF("livekit", "Cartesia STT done received", map[string]any{
				"provider": "cartesia",
			})
			return

		case "error":
			logger.ErrorCF("livekit", "Cartesia STT error response", map[string]any{
				"provider":   "cartesia",
				"request_id": msg.RequestID,
				"error":      msg.Error,
				"raw":        string(data),
			})
			return
		}
	}
}

func normalizeCartesiaLang(lang string) string {
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
		if len(lang) == 2 {
			return lang
		}
		return ""
	}
}
