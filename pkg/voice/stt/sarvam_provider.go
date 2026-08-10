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
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// The realtime endpoint. /speech-to-text/ws also completes a websocket handshake
// with 101 — verified 2026-08-10 — and then never returns a transcript, which is
// how this went undiagnosed: the socket looked healthy because it was open.
const sarvamSTTWebsocketURL = "wss://api.sarvam.ai/speech-to-text-realtime/ws"

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

	// Batch transport, chosen by env so both live in one build: the streaming
	// socket returned nothing on real sessions while accepting audio and staying
	// open. See sarvam_rest.go.
	if sarvamTransportIsREST() {
		logger.InfoCF("livekit", "Sarvam STT using REST transport", map[string]any{
			"provider": "sarvam", "model": model, "language": language,
			"mode": mode, "sample_rate": sampleRate,
		})
		return newSarvamRESTAdapter(ctx, apiKey, model, language, mode, sampleRate), nil
	}

	wsURL := sarvamStreamingURL()

	// Parameter names the realtime endpoint documents: language_code with an
	// underscore, and encoding rather than input_audio_codec. The previous set was
	// accepted without complaint and silently ignored.
	q := url.Values{}
	q.Set("language_code", language)
	q.Set("model", realtimeSarvamModel(model))
	q.Set("mode", mode)
	q.Set("sample_rate", strconv.Itoa(sampleRate))
	q.Set("encoding", "linear16")
	q.Set("stream_type", "balanced")

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
		conn:        conn,
		resultChan:  make(chan TranscriptEvent, 32),
		closed:      make(chan struct{}),
		language:    language,
		sampleRate:  sampleRate,
		binaryAudio: sarvamBinaryAudioFrames(),
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
	// Raw PCM in binary frames rather than base64 inside a JSON text frame.
	binaryAudio bool
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

	// encoding=linear16 on the connection says the socket carries raw
	// little-endian PCM, so audio goes as binary frames. Wrapping it in a JSON text
	// frame gave the server nothing it could decode as audio: it stayed open,
	// reported no error, and transcribed nothing.
	if s.binaryAudio {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
			return fmt.Errorf("sarvam: send audio: %w", err)
		}
		return nil
	}

	msg := map[string]any{
		"audio": map[string]any{
			"data":        base64.StdEncoding.EncodeToString(pcm),
			"sample_rate": s.sampleRate,
			"encoding":    "linear16",
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
		// Closing resultChan below is what makes RunInbound exit and the session go
		// deaf for the rest of the call, so the one thing worth knowing is who
		// called this. Six call sites close an STT stream and the logs named none
		// of them; runtime.Caller costs nothing on a once-per-stream path.
		caller := closeCallerOutsideAdapter()
		logger.WarnCF("livekit", "Sarvam STT stream closing", map[string]any{
			"provider":    "sarvam",
			"called_from": caller,
		})

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
			// Always logged, including when we closed first. Suppressing it on our
			// own close hid which side ended the stream, and a stream ending is what
			// makes the session deaf — RunInbound exits when resultChan closes.
			fields := map[string]any{"provider": "sarvam", "error": err.Error()}
			if ce, ok := err.(*websocket.CloseError); ok {
				fields["ws_close_code"] = ce.Code
				fields["ws_close_text"] = ce.Text
				fields["closed_by"] = "sarvam"
			} else {
				fields["closed_by"] = "transport"
			}
			select {
			case <-s.closed:
				fields["already_closed_locally"] = true
			default:
			}
			logger.WarnCF("livekit", "Sarvam STT read loop ended", fields)
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
		s.logDroppedMessage("unparseable_json", data)
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
			s.logDroppedMessage("unknown_signal_type", data)
			return TranscriptEvent{}, false
		}
	case "data", "transcript":
		text := strings.TrimSpace(msg.Data.Transcript)
		if text == "" {
			text = strings.TrimSpace(msg.Transcript)
		}
		if text == "" {
			// Sarvam answered, with nothing in it. Dropping this silently is why a
			// turn can hang forever: RunInbound keeps waiting for a transcript that
			// has already been and gone. Surfaced so an empty reply is diagnosable.
			s.logDroppedMessage("empty_transcript", data)
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
		s.logDroppedMessage("unknown_message_type", data)
		return TranscriptEvent{}, false
	}
}

// logDroppedMessage records a reply the parser could not turn into an event. Kept
// at debug because both deployments run with --log-level debug, and the payloads
// are small and infrequent.
func (s *sarvamStreamAdapter) logDroppedMessage(reason string, data []byte) {
	const maxRaw = 400
	raw := string(data)
	if len(raw) > maxRaw {
		raw = raw[:maxRaw] + "…"
	}
	logger.DebugCF("livekit", "Sarvam STT message dropped", map[string]any{
		"provider": "sarvam",
		"reason":   reason,
		"raw":      raw,
	})
}

// closeCallerOutsideAdapter names the code that closed the stream. Close runs
// inside closeOnce.Do, so a fixed skip depth lands in sync/once.go — as the first
// version of this diagnostic did. Walk out instead.
func closeCallerOutsideAdapter() string {
	pcs := make([]uintptr, 12)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if frame.File != "" &&
			!strings.Contains(frame.File, "/sync/") &&
			!strings.HasSuffix(frame.File, "sarvam_provider.go") {
			return fmt.Sprintf("%s:%d", frame.File, frame.Line)
		}
		if !more {
			return "unknown"
		}
	}
}

func sarvamStreamingURL() string {
	if override := strings.TrimSpace(os.Getenv("SARVAM_STT_STREAMING_URL")); override != "" {
		return override
	}
	return sarvamSTTWebsocketURL
}

// realtimeSarvamModel keeps the realtime suffix on, whatever the manager DB
// supplies. The active row still says saaras:v3, and sending a non-realtime model
// to the realtime endpoint is exactly the kind of mismatch that produced silence
// rather than an error.
// sarvamBinaryAudioFrames reports whether audio goes as raw binary frames.
// Defaults on: the JSON-text framing it replaces never produced a transcript on
// either endpoint. Set SARVAM_STT_AUDIO_FRAMES=json to go back.
func sarvamBinaryAudioFrames() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SARVAM_STT_AUDIO_FRAMES"))) {
	case "json", "text", "base64":
		return false
	default:
		return true
	}
}

func realtimeSarvamModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "saaras:v3-realtime"
	}
	if strings.Contains(strings.ToLower(model), "realtime") {
		return model
	}
	return model + "-realtime"
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
