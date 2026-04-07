package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const defaultSpeechmaticsURL = "wss://eu2.rt.speechmatics.com/v2"

// speechmaticsProvider implements STT using Speechmatics Real-Time WebSocket API
type speechmaticsProvider struct {
	apiKey   string
	model    string
	language string
}

// NewSpeechmaticsProvider creates a new Speechmatics provider
func NewSpeechmaticsProvider(apiKey, model, language string) Provider {
	if model == "" {
		model = "enhanced"
	}
	return &speechmaticsProvider{apiKey: apiKey, model: model, language: language}
}

func (p *speechmaticsProvider) Name() string { return "speechmatics" }
func (p *speechmaticsProvider) WithConfig(apiKey, model string) Provider {
	return NewSpeechmaticsProvider(apiKey, model, p.language)
}
func (p *speechmaticsProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "es", "fr", "de", "it", "pt", "nl", "hi", "ja", "ko", "zh", "auto"},
		Models:               []string{"enhanced", "standard"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *speechmaticsProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := p.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SPEECHMATICS_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("speechmatics: API key not configured")
	}

	language := opts.Language
	if language == "" {
		language = p.language
	}
	if language == "" {
		language = "en"
	}
	// Speechmatics uses ISO language codes
	language = normalizeLangCode(language)

	sampleRate := opts.SampleRate
	if sampleRate == 0 {
		sampleRate = 16000
	}

	model := p.model
	if opts.Model != "" {
		model = opts.Model
	}
	// Normalize to valid operating_point: "standard" or "enhanced"
	switch model {
	case "standard", "enhanced":
		// valid
	default:
		model = "enhanced"
	}

	// Connect to Speechmatics RT WebSocket
	dialer := websocket.DefaultDialer
	header := http.Header{
		"Authorization": {"Bearer " + apiKey},
	}

	conn, _, err := dialer.DialContext(ctx, defaultSpeechmaticsURL, header)
	if err != nil {
		return nil, fmt.Errorf("speechmatics: websocket dial: %w", err)
	}

	stream := &speechmaticsStream{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		ctx:        ctx,
	}

	// Send StartRecognition
	startMsg := speechmaticsStartRecognition{
		Message: "StartRecognition",
		AudioFormat: speechmaticsAudioFormat{
			Type:       "raw",
			Encoding:   "pcm_s16le",
			SampleRate: sampleRate,
		},
		TranscriptionConfig: speechmaticsTranscriptionConfig{
			Language:       language,
			EnablePartials: opts.InterimResults,
			OperatingPoint: model,
			MaxDelay:       0.7, // lowest latency
			MaxDelayMode:   "flexible",
		},
	}

	startData, err := json.Marshal(startMsg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("speechmatics: marshal StartRecognition: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, startData); err != nil {
		conn.Close()
		return nil, fmt.Errorf("speechmatics: send StartRecognition: %w", err)
	}

	// Wait for RecognitionStarted (skip Info/Warning messages that come first)
	for {
		_, respData, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("speechmatics: read RecognitionStarted: %w", err)
		}

		var resp struct {
			Message string `json:"message"`
			Type    string `json:"type,omitempty"`
			Reason  string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal(respData, &resp); err != nil {
			conn.Close()
			return nil, fmt.Errorf("speechmatics: unmarshal response: %w", err)
		}

		switch resp.Message {
		case "RecognitionStarted":
			logger.DebugCF("livekit", "Speechmatics RT session started", map[string]any{
				"language": language,
				"model":    model,
			})
			goto ready
		case "Info":
			logger.DebugCF("livekit", "Speechmatics info", map[string]any{
				"type":   resp.Type,
				"reason": resp.Reason,
			})
			continue // skip and wait for RecognitionStarted
		case "Warning":
			logger.WarnCF("livekit", "Speechmatics warning during init", map[string]any{
				"type":   resp.Type,
				"reason": resp.Reason,
			})
			continue
		case "Error":
			conn.Close()
			return nil, fmt.Errorf("speechmatics: server error: %s - %s", resp.Type, resp.Reason)
		default:
			conn.Close()
			return nil, fmt.Errorf("speechmatics: unexpected message during init: %s", string(respData))
		}
	}
ready:

	// Start read loop
	go stream.readLoop()

	return stream, nil
}

// --- WebSocket stream implementation ---

type speechmaticsStream struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	ctx        context.Context
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *speechmaticsStream) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("speechmatics stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Speechmatics AddAudio: send raw binary PCM directly
	return s.conn.WriteMessage(websocket.BinaryMessage, pcm)
}

func (s *speechmaticsStream) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *speechmaticsStream) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("speechmatics stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Send ForceEndOfUtterance to flush pending results
	msg := map[string]string{"message": "ForceEndOfUtterance"}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *speechmaticsStream) Close() error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		// Send EndOfStream
		endMsg := map[string]interface{}{
			"message":     "EndOfStream",
			"last_seq_no": 0,
		}
		endData, _ := json.Marshal(endMsg)
		_ = s.conn.WriteMessage(websocket.TextMessage, endData)
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *speechmaticsStream) readLoop() {
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
				// Expected closure
			default:
				logger.WarnCF("livekit", "Speechmatics read error", map[string]any{
					"error": err.Error(),
				})
			}
			return
		}

		var msg speechmaticsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Message {
		case "AudioAdded":
			// Acknowledgement, ignore

		case "AddPartialTranscript":
			text := strings.TrimSpace(msg.Metadata.Transcript)
			if text == "" {
				continue
			}

			evt := TranscriptEvent{
				Text:    text,
				IsFinal: false,
			}
			if !s.speaking {
				evt.SpeechStart = true
				s.speaking = true
			}

			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "AddTranscript":
			text := strings.TrimSpace(msg.Metadata.Transcript)
			if text == "" {
				continue
			}

			evt := TranscriptEvent{
				Text:    text,
				IsFinal: true,
			}
			if !s.speaking {
				evt.SpeechStart = true
				s.speaking = true
			}

			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "EndOfUtterance":
			// Speaker stopped talking
			if s.speaking {
				evt := TranscriptEvent{
					SpeechEnd: true,
				}
				s.speaking = false
				select {
				case s.resultChan <- evt:
				case <-s.closed:
					return
				}
			}

		case "EndOfStream":
			return

		case "Error":
			logger.ErrorCF("livekit", "Speechmatics error", map[string]any{
				"type":   msg.Type,
				"reason": msg.Reason,
				"raw":    string(data),
			})
			return

		case "Warning":
			logger.WarnCF("livekit", "Speechmatics warning", map[string]any{
				"type":   msg.Type,
				"reason": msg.Reason,
			})
		}
	}
}

// --- Helper: normalize language name to ISO code ---

func normalizeLangCode(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	switch lang {
	case "english", "en":
		return "en"
	case "hindi", "hi":
		return "hi"
	case "spanish", "es":
		return "es"
	case "french", "fr":
		return "fr"
	case "german", "de":
		return "de"
	case "italian", "it":
		return "it"
	case "portuguese", "pt":
		return "pt"
	case "japanese", "ja":
		return "ja"
	case "korean", "ko":
		return "ko"
	case "chinese", "mandarin", "zh":
		return "zh"
	case "dutch", "nl":
		return "nl"
	case "auto":
		return "auto"
	default:
		// If it's already a 2-letter code, return as-is
		if len(lang) == 2 {
			return lang
		}
		return "en"
	}
}

// --- JSON message types ---

type speechmaticsStartRecognition struct {
	Message             string                          `json:"message"`
	AudioFormat         speechmaticsAudioFormat         `json:"audio_format"`
	TranscriptionConfig speechmaticsTranscriptionConfig `json:"transcription_config"`
}

type speechmaticsAudioFormat struct {
	Type       string `json:"type"`
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
}

type speechmaticsTranscriptionConfig struct {
	Language       string  `json:"language"`
	EnablePartials bool    `json:"enable_partials"`
	OperatingPoint string  `json:"operating_point,omitempty"`
	MaxDelay       float64 `json:"max_delay,omitempty"`
	MaxDelayMode   string  `json:"max_delay_mode,omitempty"`
}

type speechmaticsMessage struct {
	Message  string `json:"message"`
	Type     string `json:"type,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Metadata struct {
		Transcript string `json:"transcript"`
	} `json:"metadata,omitempty"`
}
