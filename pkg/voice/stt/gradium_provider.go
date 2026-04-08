package stt

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const gradiumDefaultSTTWebsocketURL = "wss://eu.api.gradium.ai/api/speech/asr"

// gradiumProvider implements STT using Gradium's realtime WebSocket API.
type gradiumProvider struct {
	apiKey string
	model  string
}

func NewGradiumProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = "default"
	}
	return &gradiumProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *gradiumProvider) Name() string { return "gradium" }

func (p *gradiumProvider) WithConfig(apiKey, model string) Provider {
	return NewGradiumProvider(apiKey, model)
}

func (p *gradiumProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"auto"},
		Models:               []string{"default"},
		SupportsStreaming:    true,
		SupportsDiarization:  false,
		SupportsMultilingual: true,
	}
}

func (p *gradiumProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	// Env override first, then DB-configured key.
	apiKey := strings.TrimSpace(os.Getenv("GRADIUM_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(p.apiKey)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gradium: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = "default"
	}
	inputSampleRate := opts.SampleRate
	if inputSampleRate <= 0 {
		inputSampleRate = 16000
	}

	wsURL := strings.TrimSpace(os.Getenv("GRADIUM_STT_WS_URL"))
	if wsURL == "" {
		wsURL = gradiumDefaultSTTWebsocketURL
	}

	header := http.Header{}
	header.Set("x-api-key", apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("gradium websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("gradium websocket dial failed: %w", err)
	}

	// Per Gradium docs, setup must be the first message.
	setup := map[string]any{
		"type":         "setup",
		"model_name":   model,
		"input_format": "pcm",
	}
	if lang := normalizeGradiumLang(opts.Language); lang != "" {
		setup["json_config"] = map[string]any{"language": lang}
	}
	setupData, err := json.Marshal(setup)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gradium: marshal setup: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, setupData); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gradium: send setup: %w", err)
	}

	stream := &gradiumStream{
		inputSampleRate:  inputSampleRate,
		outputSampleRate: 24000,
		frameBytes:       1920 * 2, // 1920 samples @ 24kHz, 16-bit mono
		txBuf:            make([]byte, 0, 1920*4),
		readyCh:          make(chan struct{}),
		conn:             conn,
		resultChan:       make(chan TranscriptEvent, 32),
		closed:           make(chan struct{}),
		language:         normalizeGradiumLang(opts.Language),
	}

	logger.DebugCF("livekit", "Gradium STT websocket opened", map[string]any{
		"provider": "gradium",
		"ws_url":   wsURL,
		"model":    model,
	})

	go stream.readLoop()
	return stream, nil
}

type gradiumStream struct {
	inputSampleRate  int
	outputSampleRate int
	frameBytes       int
	txBuf            []byte
	readyCh          chan struct{}
	ready            bool
	conn             *websocket.Conn
	resultChan       chan TranscriptEvent
	closed           chan struct{}
	language         string
	lastErr          string
	mu               sync.Mutex
	closeOnce        sync.Once

	speaking   bool
	utterance  strings.Builder
	flushSeqNo int64
}

func (s *gradiumStream) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	select {
	case <-s.closed:
		return fmt.Errorf("gradium stream closed: %s", s.getLastErr())
	default:
	}

	if err := s.waitReady(2 * time.Second); err != nil {
		return err
	}

	encodedPCM := pcm
	if s.inputSampleRate > 0 && s.outputSampleRate > 0 && s.inputSampleRate != s.outputSampleRate {
		encodedPCM = resamplePCM16(pcm, s.inputSampleRate, s.outputSampleRate)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.txBuf = append(s.txBuf, encodedPCM...)
	for len(s.txBuf) >= s.frameBytes {
		frame := s.txBuf[:s.frameBytes]
		if err := s.sendAudioFrameLocked(frame); err != nil {
			return err
		}
		s.txBuf = s.txBuf[s.frameBytes:]
	}
	return nil
}

func (s *gradiumStream) Results() <-chan TranscriptEvent {
	return s.resultChan
}

func (s *gradiumStream) Finalize() error {
	select {
	case <-s.closed:
		return fmt.Errorf("gradium stream closed: %s", s.getLastErr())
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Send any remaining buffered audio chunk before flush.
	if len(s.txBuf) > 0 {
		if err := s.sendAudioFrameLocked(s.txBuf); err != nil {
			return err
		}
		s.txBuf = s.txBuf[:0]
	}

	s.flushSeqNo++
	flushID := strconv.FormatInt(s.flushSeqNo, 10)
	msg := map[string]any{
		"type":     "flush",
		"flush_id": flushID,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("gradium: marshal flush: %w", err)
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("gradium: send flush: %w", err)
	}
	return nil
}

func (s *gradiumStream) Close() error {
	var retErr error
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		eos := map[string]any{"type": "end_of_stream"}
		if data, err := json.Marshal(eos); err == nil {
			_ = s.conn.WriteMessage(websocket.TextMessage, data)
		}
		_ = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		retErr = s.conn.Close()
		s.mu.Unlock()

		close(s.resultChan)
	})
	return retErr
}

func (s *gradiumStream) readLoop() {
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
			s.setLastErr(err.Error())
			select {
			case <-s.closed:
			default:
				logger.WarnCF("livekit", "Gradium STT read error", map[string]any{
					"provider": "gradium",
					"error":    err.Error(),
				})
			}
			return
		}

		var msg struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Message string          `json:"message"`
			Code    int             `json:"code"`
			VAD     json.RawMessage `json:"vad"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "ready":
			s.setReady()
			logger.DebugCF("livekit", "Gradium STT ready", map[string]any{
				"provider": "gradium",
				"raw":      string(data),
			})

		case "text":
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			// Gradium emits progressive text chunks. Keep buffering internally and
			// only emit a final transcript on flush completion.
			if s.utterance.Len() > 0 {
				s.utterance.WriteString(" ")
			}
			s.utterance.WriteString(text)

			evt := TranscriptEvent{
				Text:     text,
				IsFinal:  false,
				Language: s.language,
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

		case "end_text":
			// Gradium may emit end_text multiple times while still producing text chunks.
			// Do not close utterance on end_text; rely on explicit flush response.
			continue

		case "flushed", "flush_done":
			finalText := strings.TrimSpace(s.utterance.String())
			s.utterance.Reset()
			evt := TranscriptEvent{
				Text:      finalText,
				IsFinal:   true,
				SpeechEnd: true,
				Language:  s.language,
			}
			s.speaking = false
			if evt.Text == "" {
				continue
			}
			select {
			case s.resultChan <- evt:
			case <-s.closed:
				return
			}

		case "error":
			s.setLastErr(msg.Message)
			logger.ErrorCF("livekit", "Gradium STT error response", map[string]any{
				"provider": "gradium",
				"code":     msg.Code,
				"message":  msg.Message,
				"raw":      string(data),
			})
			return

		case "end_of_stream":
			s.setLastErr("remote end_of_stream")
			return
		}
	}
}

func (s *gradiumStream) sendAudioFrameLocked(frame []byte) error {
	msg := map[string]any{
		"type":  "audio",
		"audio": base64.StdEncoding.EncodeToString(frame),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("gradium: marshal audio message: %w", err)
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("gradium: send audio: %w", err)
	}
	return nil
}

func (s *gradiumStream) waitReady(timeout time.Duration) error {
	s.mu.Lock()
	ready := s.ready
	readyCh := s.readyCh
	s.mu.Unlock()
	if ready {
		return nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-readyCh:
		return nil
	case <-s.closed:
		return fmt.Errorf("gradium stream closed before ready: %s", s.getLastErr())
	case <-timer.C:
		return fmt.Errorf("gradium stream did not become ready within %s", timeout.String())
	}
}

func (s *gradiumStream) setReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return
	}
	s.ready = true
	close(s.readyCh)
}

func (s *gradiumStream) setLastErr(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.lastErr = msg
}

func (s *gradiumStream) getLastErr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErr == "" {
		return "unknown"
	}
	return s.lastErr
}

func normalizeGradiumLang(lang string) string {
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

func resamplePCM16(pcm []byte, inputRate, outputRate int) []byte {
	if inputRate <= 0 || outputRate <= 0 || inputRate == outputRate || len(pcm) < 4 {
		return pcm
	}

	inCount := len(pcm) / 2
	outCount := int(math.Round(float64(inCount) * float64(outputRate) / float64(inputRate)))
	if outCount <= 1 {
		return pcm
	}

	inSamples := make([]int16, inCount)
	for i := 0; i < inCount; i++ {
		inSamples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
	}

	out := make([]byte, outCount*2)
	scale := float64(inputRate) / float64(outputRate)
	last := inCount - 1
	for i := 0; i < outCount; i++ {
		src := float64(i) * scale
		i0 := int(src)
		if i0 >= last {
			binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(inSamples[last]))
			continue
		}
		i1 := i0 + 1
		frac := src - float64(i0)
		s0 := float64(inSamples[i0])
		s1 := float64(inSamples[i1])
		v := int16(math.Round(s0 + (s1-s0)*frac))
		binary.LittleEndian.PutUint16(out[i*2:i*2+2], uint16(v))
	}
	return out
}
