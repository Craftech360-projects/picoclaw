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
		Models:               []string{"saaras:v3-realtime"},
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

	// The documented shape: a JSON text frame with event=audio_input and the base64
	// PCM as a plain string. This previously sent {"audio":{"data":…}} with no
	// event field, which the server accepted, ignored, and never answered. Raw
	// binary frames were also tried and are equally wrong — the spec is text+base64.
	data, err := json.Marshal(map[string]string{
		"event": "audio_input",
		"audio": base64.StdEncoding.EncodeToString(pcm),
	})
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

	// event, not type. The old key meant no finalize ever reached the server.
	data, err := json.Marshal(map[string]string{"event": "flush"})
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

// parseMessage maps a server frame to a TranscriptEvent.
//
// Dispatches on "event", which is what the realtime API sends. This used to
// switch on "type" and look for data/transcript/events/speech_start, so not one
// branch could ever match a real reply — including session.begin, which arrives
// on every successful connection.
func (s *sarvamStreamAdapter) parseMessage(data []byte) (TranscriptEvent, bool) {
	var msg struct {
		Event              string    `json:"event"`
		Text               string    `json:"text"`
		Language           string    `json:"language"`
		LanguageConfidence flexFloat `json:"language_confidence"`
		UtteranceIdx       int       `json:"utterance_idx"`
		StartS             flexFloat `json:"start_s"`
		EndS               flexFloat `json:"end_s"`
		Confidence         flexFloat `json:"confidence"`
		Code               string    `json:"code"`
		Message            string    `json:"message"`
		IsFatal            bool      `json:"is_fatal"`
		RequestID          string    `json:"request_id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		s.logDroppedMessage("unparseable_json", data)
		return TranscriptEvent{}, false
	}

	language := strings.TrimSpace(msg.Language)
	if language == "" {
		language = s.language
	}

	switch strings.ToLower(strings.TrimSpace(msg.Event)) {
	case "session.begin":
		// Proof the connection is really established, as opposed to merely open.
		logger.DebugCF("livekit", "Sarvam STT session begin", map[string]any{
			"provider": "sarvam", "request_id": msg.RequestID,
		})
		return TranscriptEvent{}, false

	case "vad.speech_start":
		s.speaking = true
		return TranscriptEvent{SpeechStart: true, Language: language}, true

	case "vad.speech_end":
		// No IsFinal here: the text arrives separately in transcript.final, and
		// claiming finality without text ends a turn with nothing in it.
		s.speaking = false
		return TranscriptEvent{SpeechEnd: true, Language: language}, true

	case "transcript.partial":
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			s.logDroppedMessage("empty_partial", data)
			return TranscriptEvent{}, false
		}
		evt := TranscriptEvent{Text: text, Language: language}
		if !s.speaking {
			s.speaking = true
			evt.SpeechStart = true
		}
		return evt, true

	case "transcript.final":
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			s.logDroppedMessage("empty_final", data)
			return TranscriptEvent{}, false
		}
		evt := TranscriptEvent{
			Text:      text,
			IsFinal:   true,
			SpeechEnd: true,
			Language:  language,
			Duration:  sarvamSpan(msg.StartS, msg.EndS),
		}
		if !s.speaking {
			evt.SpeechStart = true
		}
		s.speaking = false
		return evt, true

	case "error":
		logger.ErrorCF("livekit", "Sarvam STT error event", map[string]any{
			"provider": "sarvam",
			"code":     msg.Code,
			"is_fatal": msg.IsFatal,
			"message":  firstNonEmpty(msg.Message, msg.Code),
			"raw":      truncateForLog(string(data), 400),
		})
		return TranscriptEvent{}, false

	case "session.end", "pong", "config.updated":
		logger.DebugCF("livekit", "Sarvam STT control event", map[string]any{
			"provider": "sarvam", "event": msg.Event,
			"raw": truncateForLog(string(data), 200),
		})
		return TranscriptEvent{}, false

	default:
		s.logDroppedMessage("unknown_event", data)
		return TranscriptEvent{}, false
	}
}

// truncateForLog bounds a raw payload so a diagnostic cannot dump an unbounded
// message into the log.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// flexFloat accepts a JSON number or a quoted number.
//
// The docs show these fields quoted ("confidence":"0.95") and the live server
// sends them bare ("confidence":0.37). A plain string field makes Unmarshal fail
// and the whole message get discarded — which is how a valid vad.speech_start was
// logged as unparseable_json. Silent drops on a type mismatch are the exact
// failure class this provider has already lost days to.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	trimmed := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if trimmed == "" || trimmed == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		// Unreadable is not fatal: losing one confidence value must not cost the
		// transcript that came with it.
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

// sarvamSpan turns the start_s/end_s pair into a duration in seconds.
func sarvamSpan(startS, endS flexFloat) float64 {
	if endS <= startS {
		return 0
	}
	return float64(endS - startS)
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
		// Never "auto": accepted by the endpoint but it then sends only partials, so
		// turns never finalise. en-IN is the product's primary; a Hindi or other
		// session passes its own language through the cases below and is unaffected.
		// "unknown" is rejected outright by the realtime endpoint (close 4000).
		return "en-IN"
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
		// or-IN. The accepted list has no od-IN, so the old value would be rejected
		// the same way "unknown" was.
		return "or-IN"
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
