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
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const sarvamSTTWebsocketURL = "wss://api.sarvam.ai/speech-to-text/ws"

// sarvamProvider implements STT using Sarvam's streaming WebSocket API.
type sarvamProvider struct {
	apiKey string
	model  string
}

func NewSarvamProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "saaras:v3"
	}
	return &sarvamProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *sarvamProvider) Name() string { return "sarvam" }

func (p *sarvamProvider) WithConfig(apiKey, model string) Provider {
	return NewSarvamProvider(apiKey, model)
}

func (p *sarvamProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages: []string{
			"auto", "unknown",
			"hi-IN", "bn-IN", "gu-IN", "kn-IN", "ml-IN", "mr-IN", "od-IN", "pa-IN", "ta-IN", "te-IN", "en-IN",
			"as-IN", "ur-IN", "ne-IN", "kok-IN", "ks-IN", "sd-IN", "sa-IN", "sat-IN", "mni-IN", "brx-IN", "mai-IN", "doi-IN",
		},
		Models:               []string{"saaras:v3", "saarika:v2.5"},
		SupportsStreaming:    true,
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *sarvamProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("SARVAM_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("sarvam: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = "saaras:v3"
	}

	sampleRate := normalizeSarvamSampleRate(opts.SampleRate)
	language := normalizeSarvamLang(opts.Language)
	mode := normalizeSarvamMode(os.Getenv("SARVAM_STT_MODE"))
	wsURL := sarvamStreamingURL()

	q := url.Values{}
	q.Set("language-code", language)
	q.Set("model", model)
	q.Set("mode", mode)
	q.Set("sample_rate", strconv.Itoa(sampleRate))
	q.Set("input_audio_codec", "pcm_s16le")
	q.Set("flush_signal", "true")
	q.Set("vad_signals", "true")
	if endpointMS := opts.EndpointingMS; endpointMS > 0 && endpointMS <= 700 {
		q.Set("high_vad_sensitivity", "true")
	}

	connURL := wsURL + "?" + q.Encode()
	header := http.Header{}
	header.Set("Api-Subscription-Key", apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, connURL, header)
	if err != nil {
		if resp != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(bodyBytes) > 0 {
				return nil, fmt.Errorf("sarvam websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(bodyBytes)))
			}
			return nil, fmt.Errorf("sarvam websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("sarvam websocket dial failed: %w", err)
	}

	stream := &sarvamStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   language,
		sampleRate: sampleRate,
	}

	logger.DebugCF("livekit", "Sarvam STT websocket opened", map[string]any{
		"provider":    "sarvam",
		"model":       model,
		"mode":        mode,
		"language":    language,
		"sample_rate": sampleRate,
	})

	go stream.readLoop()
	return stream, nil
}

type sarvamStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string
	sampleRate int
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *sarvamStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("sarvam stream closed")
	default:
	}

	msg := map[string]any{
		"audio": map[string]any{
			"data":        base64.StdEncoding.EncodeToString(pcm),
			"sample_rate": s.sampleRate,
			"encoding":    "audio/wav",
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("sarvam: marshal audio message: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("sarvam: send audio: %w", err)
	}
	return nil
}

func (s *sarvamStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *sarvamStreamAdapter) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("sarvam stream closed")
	default:
	}

	data, err := json.Marshal(map[string]string{"type": "flush"})
	if err != nil {
		return fmt.Errorf("sarvam: marshal flush message: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("sarvam: send flush: %w", err)
	}
	logger.DebugCF("livekit", "Sarvam flush sent", map[string]any{"provider": "sarvam"})
	return nil
}

func (s *sarvamStreamAdapter) Close() error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *sarvamStreamAdapter) readLoop() {
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
				logger.WarnCF("livekit", "Sarvam STT read error", map[string]any{
					"provider": "sarvam",
					"error":    err.Error(),
				})
			}
			return
		}

		evt, ok := s.parseMessage(data)
		if !ok {
			continue
		}

		select {
		case s.resultChan <- evt:
		case <-s.closed:
			return
		}
	}
}

func (s *sarvamStreamAdapter) parseMessage(data []byte) (TranscriptEvent, bool) {
	var msg struct {
		Type string `json:"type"`
		Data struct {
			RequestID    string `json:"request_id"`
			Transcript   string `json:"transcript"`
			LanguageCode string `json:"language_code"`
			SignalType   string `json:"signal_type"`
			Message      string `json:"message"`
			Metrics      struct {
				AudioDuration     float64 `json:"audio_duration"`
				ProcessingLatency float64 `json:"processing_latency"`
			} `json:"metrics"`
		} `json:"data"`
		Transcript   string `json:"transcript"`
		LanguageCode string `json:"language_code"`
		Error        string `json:"error"`
		Message      string `json:"message"`
		SignalType   string `json:"signal_type"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return TranscriptEvent{}, false
	}

	switch strings.ToLower(strings.TrimSpace(msg.Type)) {
	case "speech_start":
		s.speaking = true
		return TranscriptEvent{SpeechStart: true, Language: s.language}, true
	case "speech_end":
		s.speaking = false
		return TranscriptEvent{IsFinal: true, SpeechEnd: true, Language: s.language}, true
	case "events":
		switch strings.ToUpper(firstNonEmpty(msg.Data.SignalType, msg.SignalType)) {
		case "START_SPEECH":
			s.speaking = true
			return TranscriptEvent{SpeechStart: true, Language: s.language}, true
		case "END_SPEECH":
			s.speaking = false
			return TranscriptEvent{IsFinal: true, SpeechEnd: true, Language: s.language}, true
		default:
			return TranscriptEvent{}, false
		}
	case "data", "transcript":
		text := strings.TrimSpace(msg.Data.Transcript)
		if text == "" {
			text = strings.TrimSpace(msg.Transcript)
		}
		if text == "" {
			return TranscriptEvent{}, false
		}
		lang := strings.TrimSpace(msg.Data.LanguageCode)
		if lang == "" {
			lang = strings.TrimSpace(msg.LanguageCode)
		}
		if lang == "" {
			lang = s.language
		}

		evt := TranscriptEvent{
			Text:      text,
			IsFinal:   true,
			SpeechEnd: true,
			Language:  lang,
			Duration:  msg.Data.Metrics.AudioDuration,
		}
		if !s.speaking {
			evt.SpeechStart = true
		}
		s.speaking = false
		return evt, true
	case "error":
		logger.ErrorCF("livekit", "Sarvam STT error response", map[string]any{
			"provider": "sarvam",
			"error":    firstNonEmpty(msg.Error, msg.Message, msg.Data.Message),
			"raw":      string(data),
		})
		return TranscriptEvent{}, false
	default:
		return TranscriptEvent{}, false
	}
}

func sarvamStreamingURL() string {
	if override := strings.TrimSpace(os.Getenv("SARVAM_STT_STREAMING_URL")); override != "" {
		return override
	}
	return sarvamSTTWebsocketURL
}

func normalizeSarvamSampleRate(sampleRate int) int {
	switch sampleRate {
	case 8000, 16000:
		return sampleRate
	default:
		return 16000
	}
}

func normalizeSarvamMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "translate", "verbatim", "translit", "codemix":
		return strings.TrimSpace(strings.ToLower(mode))
	default:
		return "transcribe"
	}
}

func normalizeSarvamLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "", "auto", "unknown":
		return "unknown"
	case "english", "en":
		return "en-IN"
	case "hindi", "hi":
		return "hi-IN"
	case "bengali", "bn":
		return "bn-IN"
	case "gujarati", "gu":
		return "gu-IN"
	case "kannada", "kn":
		return "kn-IN"
	case "malayalam", "ml":
		return "ml-IN"
	case "marathi", "mr":
		return "mr-IN"
	case "odia", "or", "od":
		return "od-IN"
	case "punjabi", "pa":
		return "pa-IN"
	case "tamil", "ta":
		return "ta-IN"
	case "telugu", "te":
		return "te-IN"
	default:
		// Pass through valid BCP-47 style values (e.g. hi-IN, en-IN, ur-IN).
		if strings.Contains(lang, "-") {
			parts := strings.SplitN(lang, "-", 2)
			return strings.ToLower(parts[0]) + "-" + strings.ToUpper(parts[1])
		}
		return "unknown"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
