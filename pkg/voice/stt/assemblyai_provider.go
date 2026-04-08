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

const assemblyAIStreamingURL = "wss://streaming.assemblyai.com/v3/ws"

// assemblyaiProvider implements STT using AssemblyAI realtime WebSocket API.
type assemblyaiProvider struct {
	apiKey string
	model  string
}

// NewAssemblyAIProvider creates a new AssemblyAI provider.
func NewAssemblyAIProvider(apiKey, model string) Provider {
	if model == "" {
		model = "u3-rt-pro"
	}
	return &assemblyaiProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *assemblyaiProvider) Name() string { return "assemblyai" }

func (p *assemblyaiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto", "en", "es", "de", "fr", "pt", "it"},
		Models:               []string{"u3-rt-pro", "universal-streaming-english", "universal-streaming-multilingual", "whisper-rt"},
		SupportsStreaming:    true,
		SupportsDiarization:  true,
		SupportsMultilingual: true,
	}
}

func (p *assemblyaiProvider) WithConfig(apiKey, model string) Provider {
	return NewAssemblyAIProvider(apiKey, model)
}

func (p *assemblyaiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("ASSEMBLYAI_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("assemblyai: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	model = normalizeAssemblyAIStreamingModel(model)

	sampleRate := opts.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	channels := opts.Channels
	if channels <= 0 {
		channels = 1
	}

	minTurnSilence, maxTurnSilence := assemblyAITurnSilence(model, opts.EndpointingMS)

	q := url.Values{}
	q.Set("sample_rate", strconv.Itoa(sampleRate))
	q.Set("speech_model", model)
	q.Set("encoding", "pcm_s16le")
	q.Set("min_turn_silence", strconv.Itoa(minTurnSilence))
	q.Set("max_turn_silence", strconv.Itoa(maxTurnSilence))
	q.Set("speaker_labels", "false")

	wsURL := assemblyAIStreamingBaseURL() + "?" + q.Encode()
	header := http.Header{}
	header.Set("Authorization", apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(bodyBytes) > 0 {
				return nil, fmt.Errorf("assemblyai websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(bodyBytes)))
			}
			return nil, fmt.Errorf("assemblyai websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("assemblyai websocket dial failed: %w", err)
	}

	stream := &assemblyaiStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		frameBytes: assemblyAIFrameBytes(sampleRate, channels),
		pendingPCM: make([]byte, 0),
	}

	logger.DebugCF("livekit", "AssemblyAI websocket opened", map[string]any{
		"provider":          "assemblyai",
		"model":             model,
		"sample_rate":       sampleRate,
		"channels":          channels,
		"min_turn_silence":  minTurnSilence,
		"max_turn_silence":  maxTurnSilence,
		"frame_bytes_50_ms": stream.frameBytes,
	})

	go stream.readLoop()
	return stream, nil
}

type assemblyaiStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	frameBytes int
	pendingPCM []byte
	mu         sync.Mutex
	closeOnce  sync.Once
	speaking   bool
}

func (s *assemblyaiStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("assemblyai stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pendingPCM = append(s.pendingPCM, pcm...)
	for len(s.pendingPCM) >= s.frameBytes {
		if err := s.conn.WriteMessage(websocket.BinaryMessage, s.pendingPCM[:s.frameBytes]); err != nil {
			return fmt.Errorf("assemblyai send audio: %w", err)
		}
		s.pendingPCM = s.pendingPCM[s.frameBytes:]
	}
	return nil
}

func (s *assemblyaiStreamAdapter) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *assemblyaiStreamAdapter) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("assemblyai stream closed")
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pendingPCM) > 0 {
		if len(s.pendingPCM) < s.frameBytes {
			padding := make([]byte, s.frameBytes-len(s.pendingPCM))
			s.pendingPCM = append(s.pendingPCM, padding...)
		}
		if err := s.conn.WriteMessage(websocket.BinaryMessage, s.pendingPCM); err != nil {
			return fmt.Errorf("assemblyai flush audio: %w", err)
		}
		s.pendingPCM = s.pendingPCM[:0]
	}

	if err := s.conn.WriteJSON(map[string]string{"type": "ForceEndpoint"}); err != nil {
		return fmt.Errorf("assemblyai force endpoint: %w", err)
	}
	return nil
}

func (s *assemblyaiStreamAdapter) Close() error {
	return s.close(true)
}

func (s *assemblyaiStreamAdapter) close(sendTerminate bool) error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		if sendTerminate {
			_ = s.conn.WriteJSON(map[string]string{"type": "Terminate"})
		}
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()
	})
	return retErr
}

func (s *assemblyaiStreamAdapter) readLoop() {
	defer func() {
		_ = s.close(false)
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
			default:
				logger.WarnCF("livekit", "AssemblyAI read error", map[string]any{
					"provider": "assemblyai",
					"error":    err.Error(),
				})
			}
			return
		}

		var msg struct {
			Type                 string  `json:"type"`
			ID                   string  `json:"id"`
			Transcript           string  `json:"transcript"`
			EndOfTurn            bool    `json:"end_of_turn"`
			Confidence           float64 `json:"confidence"`
			LanguageCode         string  `json:"language_code"`
			AudioDurationSeconds float64 `json:"audio_duration_seconds"`
			SessionDuration      float64 `json:"session_duration_seconds"`
			Error                string  `json:"error"`
			Message              string  `json:"message"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch strings.ToLower(msg.Type) {
		case "begin":
			logger.DebugCF("livekit", "AssemblyAI session started", map[string]any{
				"provider":   "assemblyai",
				"session_id": msg.ID,
			})

		case "speechstarted", "speech_started":
			s.speaking = true
			select {
			case s.resultChan <- TranscriptEvent{SpeechStart: true}:
			case <-s.closed:
				return
			}

		case "turn":
			text := strings.TrimSpace(msg.Transcript)
			evt := TranscriptEvent{
				Text:       text,
				IsFinal:    msg.EndOfTurn,
				SpeechEnd:  msg.EndOfTurn,
				Confidence: msg.Confidence,
				Language:   strings.TrimSpace(msg.LanguageCode),
			}
			if text != "" && !s.speaking {
				evt.SpeechStart = true
				s.speaking = true
			}
			if msg.EndOfTurn {
				s.speaking = false
			}
			if evt.Text == "" && !evt.SpeechEnd && !evt.SpeechStart {
				continue
			}

			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "termination":
			logger.DebugCF("livekit", "AssemblyAI session terminated", map[string]any{
				"provider":               "assemblyai",
				"audio_duration_seconds": msg.AudioDurationSeconds,
				"session_duration_secs":  msg.SessionDuration,
			})
			return

		case "error":
			errText := strings.TrimSpace(msg.Error)
			if errText == "" {
				errText = strings.TrimSpace(msg.Message)
			}
			logger.ErrorCF("livekit", "AssemblyAI websocket error", map[string]any{
				"provider": "assemblyai",
				"error":    errText,
				"raw":      string(data),
			})
			return
		}
	}
}

func assemblyAIStreamingBaseURL() string {
	if override := strings.TrimSpace(os.Getenv("ASSEMBLYAI_STREAMING_URL")); override != "" {
		return override
	}
	return assemblyAIStreamingURL
}

func assemblyAIFrameBytes(sampleRate, channels int) int {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	frameBytes := (sampleRate * channels * 2) / 20 // 50ms PCM16 frame
	if frameBytes <= 0 {
		return 1600
	}
	return frameBytes
}

func assemblyAITurnSilence(model string, endpointingMS int) (minTurnSilence int, maxTurnSilence int) {
	if model == "u3-rt-pro" {
		minTurnSilence = 100
		maxTurnSilence = 1000
	} else {
		minTurnSilence = 400
		maxTurnSilence = 1280
	}

	if endpointingMS > 0 {
		maxTurnSilence = endpointingMS
		if maxTurnSilence < minTurnSilence {
			maxTurnSilence = minTurnSilence
		}
	}

	return minTurnSilence, maxTurnSilence
}

func normalizeAssemblyAIStreamingModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "u3-rt-pro", "universal-3-pro", "universal_pro", "best":
		return "u3-rt-pro"
	case "universal", "universal-2", "universal-streaming-english":
		return "universal-streaming-english"
	case "universal-streaming-multilingual", "multilingual":
		return "universal-streaming-multilingual"
	case "whisper-rt", "whisper", "whisper-1", "slam-1":
		return "whisper-rt"
	default:
		return strings.TrimSpace(model)
	}
}
