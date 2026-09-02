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
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// The Gemini Live API bidirectional endpoint. Auth is a `key` query parameter;
// this endpoint does not accept an Authorization header.
const geminiSTTWebsocketURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

const geminiDefaultModel = "gemini-3.5-transcribe-live"

// geminiProvider implements streaming STT on gemini-3.5-transcribe-live.
type geminiProvider struct {
	apiKey string
	model  string
}

func NewGeminiProvider(apiKey, model string) Provider {
	if strings.TrimSpace(model) == "" {
		model = geminiDefaultModel
	}
	return &geminiProvider{apiKey: apiKey, model: model}
}

func (p *geminiProvider) Name() string { return "gemini" }

func (p *geminiProvider) WithConfig(apiKey, model string) Provider {
	return NewGeminiProvider(apiKey, model)
}

func (p *geminiProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Languages:            []string{"en", "hi", "bn", "ta", "te", "mr", "gu", "kn", "ml", "pa", "auto"},
		Models:               []string{geminiDefaultModel},
		SupportsStreaming:    true,
		SupportsDiarization:  false, // live sessions carry no speaker attribution
		SupportsMultilingual: true,
	}
}

func (p *geminiProvider) OpenStream(ctx context.Context, opts StreamOptions) (TranscriptionStream, error) {
	apiKey := strings.TrimSpace(p.apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: API key not configured")
	}

	model := strings.TrimSpace(p.model)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = geminiDefaultModel
	}

	// SECRET: connURL carries the API key in its query string — this endpoint
	// accepts no Authorization header. Never log it, never fold it into an
	// error message, never attach it to a span. The dial failure below
	// deliberately reports only the HTTP status and body for that reason.
	q := url.Values{}
	q.Set("key", apiKey)
	connURL := geminiSTTURL() + "?" + q.Encode()

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, connURL, http.Header{})
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if len(body) > 0 {
				return nil, fmt.Errorf("gemini websocket dial failed: %w (status=%s body=%s)", err, resp.Status, strings.TrimSpace(string(body)))
			}
			return nil, fmt.Errorf("gemini websocket dial failed: %w (status=%s)", err, resp.Status)
		}
		return nil, fmt.Errorf("gemini websocket dial failed: %w", err)
	}

	languages := normalizeGeminiLanguages(opts.Language)
	setup := map[string]any{
		"setup": map[string]any{
			"model": "models/" + model,
			"generationConfig": map[string]any{
				"responseModalities": []string{"TEXT"},
			},
			"inputAudioTranscription": map[string]any{
				// An empty slice is the documented auto-detect value.
				"languageCodes": languages,
				"mode":          "SMART",
			},
			"realtimeInputConfig": map[string]any{
				// Manual activity detection: the device's Manual Talk tap is the
				// sole Turn Boundary authority (ADR 0007). Server VAD stays off.
				"automaticActivityDetection": map[string]any{"disabled": true},
			},
		},
	}
	payload, err := json.Marshal(setup)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: marshal setup: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: write setup: %w", err)
	}

	// Block on setupComplete so a rejected model or bad key surfaces here, at
	// OpenStream, rather than as a silent socket that never transcribes.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: awaiting setupComplete: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	// Decoded, not substring-matched: an error frame that happens to quote the
	// field name ("unknown field setupComplete", say) would pass a Contains
	// check and leave us treating a rejection as a live session.
	var ack map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: setup response not JSON: %s", truncateForLog(string(raw), 512))
	}
	if _, ok := ack["setupComplete"]; !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("gemini: setup rejected: %s", truncateForLog(string(raw), 512))
	}

	stream := &geminiStreamAdapter{
		conn:       conn,
		resultChan: make(chan TranscriptEvent, 32),
		closed:     make(chan struct{}),
		language:   strings.Join(languages, ","),
	}

	logger.DebugCF("livekit", "Gemini STT websocket opened", map[string]any{
		"provider":  "gemini",
		"model":     model,
		"languages": languages,
	})

	go stream.readLoop()
	go stream.retireAtTTL(geminiSessionTTL(), geminiRetireBackstop())
	return stream, nil
}

type geminiStreamAdapter struct {
	conn       *websocket.Conn
	resultChan chan TranscriptEvent
	closed     chan struct{}
	language   string

	// Lock order is turnMu → writeMu, and only that way: writeJSON and Close
	// are the sole writeMu holders and neither touches turnMu, so taking
	// writeMu underneath turnMu (which ResetBuffer and Finalize do, to keep
	// the activityStart/activityEnd wire order matching the state they record)
	// cannot deadlock.
	writeMu   sync.Mutex
	closeOnce sync.Once

	turnMu        sync.Mutex
	emptyHandler  func()
	sawAudio      bool
	gotFinal      bool
	activityEnded bool
	// activityOpen tracks whether an activityStart is currently outstanding on
	// the wire, independent of turn generation — ResetBuffer is the only
	// source of activityStart (OpenStream sends none at setup), so this is
	// false until the first ResetBuffer and flips with every open/close.
	activityOpen bool
	turnGen      uint64
	// cancelledGen is the turn generation CancelTurn last marked, or 0 for
	// none. Review round 1, finding 1: a bare bool cleared by ResetBuffer let
	// a cancelled turn's late final leak out if the child pressed again before
	// it arrived. Keying suppression to the generation it belongs to, and
	// clearing it only once a LATER generation's own Finalize has sent that
	// generation's activityEnd, closes the window: everything received while
	// a cancellation is outstanding is presumed to belong to the cancelled
	// turn, right up until the next turn's own boundary is provably sent.
	cancelledGen uint64
	// retirePending is set when the session TTL elapsed while an activity
	// window was open. Closing right then would drop the child mid-utterance —
	// the exact failure the TTL exists to avoid — so the close waits for the
	// next turn boundary, with retireAtTTL's backstop closing anyway if that
	// boundary never comes.
	retirePending bool
}

func (s *geminiStreamAdapter) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.closed:
		return fmt.Errorf("gemini: stream closed")
	default:
	}
	return s.conn.WriteMessage(websocket.TextMessage, payload)
}

// SendAudio forwards a PCM frame, but only while a turn's activity window is
// open. The LiveKit track delivers audio for the whole session — silence
// between turns, room noise, and the agent's own TTS — and under manual
// activity detection (ADR 0007) the service discards anything outside a
// window anyway. Uploading it bought nothing and billed audio-input tokens
// for the session's full wall-clock rather than the child's actual speech:
// measured 53s uploaded for ~5s spoken. sarvam_rest has no equivalent problem
// because it POSTs one clip per turn.
//
// Dropping the frame also keeps sawAudio honest — it must mean "this turn
// carried speech", which is what the empty-tap reply keys on.
func (s *geminiStreamAdapter) SendAudio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	s.turnMu.Lock()
	if !s.activityOpen {
		s.turnMu.Unlock()
		return nil
	}
	s.sawAudio = true
	s.turnMu.Unlock()

	return s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{
			"audio": map[string]any{
				"data":     base64.StdEncoding.EncodeToString(pcm),
				"mimeType": "audio/pcm;rate=16000",
			},
		},
	})
}

func (s *geminiStreamAdapter) Results() <-chan TranscriptEvent { return s.resultChan }

// ResetBuffer is a fresh press: open a new activity window. It deliberately
// does NOT clear cancelledGen — see that field's comment; suppression from a
// cancelled turn survives a fast press and is cleared only once THIS new
// generation's own Finalize has sent its activityEnd.
//
// If the previous window was never closed — two presses with no Finalize
// between them (room_session.go:541-544 resets on every press), or a press
// landing inside the 200ms cancel grace before the deferred Finalize fires —
// the Live API has no open activity window to accept a second activityStart
// on, so the stale one is closed first (finding 2).
//
// The writes happen UNDER turnMu, not after releasing it. ResetBuffer runs on
// the data-message goroutine (room_session.go:545) and Finalize on RunInbound's
// (audio_pipeline.go:1808), so they are genuinely concurrent: a press landing
// between Finalize's unlock and its write would otherwise put activityStart on
// the wire ahead of the previous turn's activityEnd, closing the new window the
// instant it opened while both adapters' flags claimed all was well.
func (s *geminiStreamAdapter) ResetBuffer() {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	if s.activityOpen {
		if err := s.writeJSON(map[string]any{
			"realtimeInput": map[string]any{"activityEnd": map[string]any{}},
		}); err != nil {
			logger.DebugCF("livekit", "Gemini stale-window activityEnd failed", map[string]any{
				"provider": "gemini",
				"error":    err.Error(),
			})
		}
		s.activityOpen = false
	}

	s.sawAudio = false
	s.gotFinal = false
	s.activityEnded = false
	s.turnGen++

	if err := s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{"activityStart": map[string]any{}},
	}); err != nil {
		// Only a write that landed opens a window. Recording one that didn't
		// would have the next ResetBuffer send a stale-window activityEnd for
		// a window the server never opened.
		logger.DebugCF("livekit", "Gemini activityStart failed", map[string]any{
			"provider": "gemini",
			"error":    err.Error(),
		})
		return
	}
	s.activityOpen = true
}

// CancelTurn is deliberate silence. The audio already reached Google, so the
// final still arrives; suppress it on the way out and skip the empty-tap reply.
//
// Deliberately sends nothing: room_session.go:554-562 calls this twice per
// cancel and then drives Finalize, and Finalize owns the single activityEnd.
// Idempotent by construction: both calls record the same turnGen.
func (s *geminiStreamAdapter) CancelTurn() {
	s.turnMu.Lock()
	s.cancelledGen = s.turnGen
	s.turnMu.Unlock()
}

// SetEmptyResultHandler registers the "I didn't hear you" callback.
func (s *geminiStreamAdapter) SetEmptyResultHandler(fn func()) {
	s.turnMu.Lock()
	s.emptyHandler = fn
	s.turnMu.Unlock()
}

// geminiEmptyGrace is how long Finalize waits for a final before calling the
// tap empty.
// ponytail: one fixed window, not adaptive. If real devices show finals landing
// later than this on slow networks, widen GEMINI_STT_EMPTY_GRACE rather than
// building latency tracking.
func geminiEmptyGrace() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GEMINI_STT_EMPTY_GRACE")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 3 * time.Second
}

// geminiSessionTTL is how long a socket is used before it is retired. The Live
// API hard-caps a transcription session at 10 minutes; retiring at 9 leaves
// room for the pipeline to reopen without racing the server's own close.
func geminiSessionTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GEMINI_STT_SESSION_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 9 * time.Minute
}

// geminiRetireGrace is how long a deferred retirement waits at the turn
// boundary before closing the socket.
//
// Derived from the empty grace rather than fixed, and that is the whole point:
// Finalize spawns announceIfEmpty and retireAfterTurn together, and
// announceIfEmpty returns early on <-s.closed. Given the SAME duration the two
// race, and a Close that wins leaves an empty tap answered with silence instead
// of "I didn't hear you". A bare constant would only hold that ordering at the
// default GEMINI_STT_EMPTY_GRACE; doubling it holds at every setting, so the
// announcement always resolves first. Still far inside the backstop.
func geminiRetireGrace() time.Duration {
	return 2 * geminiEmptyGrace()
}

// geminiRetireBackstop is how long past the TTL a deferred retirement waits for
// a turn boundary before closing anyway. 45s past the 9m TTL lands at 9m45s,
// still inside the server's own 10m cap — a window left open forever must not
// be allowed to run the session into that cap.
func geminiRetireBackstop() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GEMINI_STT_RETIRE_BACKSTOP")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 45 * time.Second
}

// Finalize is End Turn: close the activity window, then decide whether the tap
// produced anything. A tap that carried audio but drew no final gets the
// empty-result reply.
//
// The activityEnd is one-shot per turn generation. Both PTT paths can drive
// more than one Finalize for a single turn — the cancel path calls CancelTurn
// twice and then finalizes, and a 25s cap can race a speech_end — and the Live
// API has no open activity window to close the second time.
//
// This is also where cancellation suppression gets released (finding 1): once
// a generation LATER than the cancelled one sends its own activityEnd here,
// any final arriving after this write on the wire is guaranteed to belong to
// the new turn, not the cancelled one, so cancelledGen is cleared.
func (s *geminiStreamAdapter) Finalize() error {
	s.turnMu.Lock()
	gen := s.turnGen
	sawAudio := s.sawAudio
	cancelledNow := s.cancelledGen != 0 && s.cancelledGen == gen
	if s.activityEnded {
		s.turnMu.Unlock()
		return nil
	}

	// Written under turnMu, and the flags committed only once the write lands
	// (findings 4 and 5). Marking the turn ended before attempting the write
	// meant a failed write — precisely the dying-socket case a rotation
	// produces — permanently marked the turn ended: a retried Finalize
	// returned nil without retrying, and the next ResetBuffer saw no stale
	// window to close either, leaving the server's window open forever.
	err := s.writeJSON(map[string]any{
		"realtimeInput": map[string]any{"activityEnd": map[string]any{}},
	})
	retireNow := false
	if err == nil {
		s.activityEnded = true
		s.activityOpen = false
		if s.cancelledGen != 0 && gen > s.cancelledGen {
			s.cancelledGen = 0
		}
		retireNow = s.retirePending
	}
	s.turnMu.Unlock()

	if sawAudio && !cancelledNow {
		go s.announceIfEmpty(gen, geminiEmptyGrace())
	}
	if retireNow {
		// The turn boundary the deferred retirement was waiting for. Grace
		// first: the final lands ~0.5s after activityEnd on the live endpoint,
		// and closing the socket ahead of it would throw away the very
		// utterance we just ended.
		go s.retireAfterTurn(gen, geminiRetireGrace())
	}
	return err
}

// announceIfEmpty fires the empty-result handler when the grace window closes
// with no final for this turn. Keyed on turnGen so a press landing during the
// wait cancels the announcement for the turn it superseded, and defensively
// re-checks cancelledGen against this specific generation (Finalize already
// gates spawning this goroutine on the turn not being the cancelled one).
func (s *geminiStreamAdapter) announceIfEmpty(gen uint64, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.closed:
		return
	}

	s.turnMu.Lock()
	stale := s.turnGen != gen
	quiet := !s.gotFinal && s.cancelledGen != gen
	handler := s.emptyHandler
	s.turnMu.Unlock()

	if stale || !quiet || handler == nil {
		return
	}
	logger.InfoCF("livekit", "Gemini tap produced no transcript", map[string]any{
		"provider": "gemini",
		"grace":    grace.String(),
	})
	handler()
}

// retireAtTTL closes the socket at the TTL. readLoop then closes resultChan,
// which audio_pipeline.go's reopenSTTStream already treats as a dead stream.
//
// Never mid-utterance: a bare timer that closed on the deadline traded the
// server's mid-turn close for one of our own, which is not a win. If a turn is
// open at the deadline the retirement is deferred to that turn's Finalize;
// backstop bounds the wait so a window left open forever cannot run the
// session into the server's own 10-minute cap.
func (s *geminiStreamAdapter) retireAtTTL(ttl, backstop time.Duration) {
	timer := time.NewTimer(ttl)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.closed:
		return
	}

	s.turnMu.Lock()
	open := s.activityOpen
	gen := s.turnGen
	s.retirePending = true
	s.turnMu.Unlock()

	if open {
		logger.InfoCF("livekit", "Gemini STT session TTL reached mid-turn; retirement deferred", map[string]any{
			"provider": "gemini",
			"ttl":      ttl.String(),
			"backstop": backstop.String(),
		})
	} else {
		// No window open, but not closed on the spot either: the live endpoint
		// delivers the final ~0.54s AFTER activityEnd, so a TTL landing in that
		// gap would discard a completed utterance the child already spoke.
		// Same grace-wait path as the deferred case, so both behave alike.
		logger.InfoCF("livekit", "Retiring Gemini STT socket at session TTL after the final grace", map[string]any{
			"provider": "gemini",
			"ttl":      ttl.String(),
		})
		go s.retireAfterTurn(gen, geminiRetireGrace())
	}

	// Armed either way. retireAfterTurn declines to close on top of a live
	// turn, so without this a session that keeps pressing would never retire.
	deadline := time.NewTimer(backstop)
	defer deadline.Stop()
	select {
	case <-deadline.C:
		logger.WarnCF("livekit", "Retiring Gemini STT socket at the TTL backstop", map[string]any{
			"provider": "gemini",
			"ttl":      ttl.String(),
			"backstop": backstop.String(),
		})
		_ = s.Close()
	case <-s.closed:
	}
}

// retireAfterTurn takes a deferred retirement once the turn that deferred it
// has had its grace to deliver a final.
//
// Re-gated on turn state at the moment it fires, not just when it was spawned.
// Nothing cancels this goroutine, so a press landing inside the grace window
// (ResetBuffer sets activityOpen again) would otherwise be closed on top of —
// the same mid-utterance close the deferral exists to prevent, narrowed to the
// grace window. A newer generation means a later Finalize has already spawned
// its own attempt, so this one is stale. retirePending is never cleared, so
// declining here only postpones: the next successful Finalize re-spawns, and
// retireAtTTL's backstop remains the hard stop.
func (s *geminiStreamAdapter) retireAfterTurn(gen uint64, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.closed:
		return
	}

	s.turnMu.Lock()
	open := s.activityOpen
	stale := s.turnGen != gen
	s.turnMu.Unlock()

	if open || stale {
		logger.DebugCF("livekit", "Deferred Gemini STT retirement declined; turn still live", map[string]any{
			"provider":      "gemini",
			"activity_open": open,
			"stale_gen":     stale,
		})
		return
	}

	logger.InfoCF("livekit", "Retiring Gemini STT socket at the turn boundary after its TTL", map[string]any{
		"provider": "gemini",
		"grace":    grace.String(),
	})
	_ = s.Close()
}

func (s *geminiStreamAdapter) Close() error {
	s.closeOnce.Do(func() {
		// Which caller closed this is the one thing worth knowing: readLoop
		// closes resultChan on the way out, and a session whose STT stream
		// dies without being reopened is a toy that has gone deaf. Same
		// diagnostic, and the same reasoning, as sarvam_provider.go:208-231.
		logger.WarnCF("livekit", "Gemini STT stream closing", map[string]any{
			"provider":    "gemini",
			"called_from": closeCallerOutsideAdapter("gemini_provider.go"),
		})

		close(s.closed)
		s.writeMu.Lock()
		_ = s.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = s.conn.Close()
		s.writeMu.Unlock()
	})
	return nil
}

func (s *geminiStreamAdapter) readLoop() {
	defer close(s.resultChan)
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			// Warn, always, and with the close code: this return closes
			// resultChan, which is what makes the session deaf until the
			// pipeline reopens. Suppressing it on our own close hid which side
			// ended the stream (sarvam_provider.go:249-263).
			fields := map[string]any{"provider": "gemini", "error": err.Error()}
			if ce, ok := err.(*websocket.CloseError); ok {
				fields["ws_close_code"] = ce.Code
				fields["ws_close_text"] = ce.Text
				fields["closed_by"] = "gemini"
			} else {
				fields["closed_by"] = "transport"
			}
			select {
			case <-s.closed:
				fields["already_closed_locally"] = true
			default:
			}
			logger.WarnCF("livekit", "Gemini STT read loop ended", fields)
			return
		}
		evt, ok := s.parseMessage(data)
		if !ok {
			continue
		}

		// Suppress FINALS only while a cancellation is outstanding, not
		// interims (review round 2). Incoming messages carry no generation
		// tag, so a stale event can only be identified by kind and timing:
		// an interim arrives during speech, which for the cancelled turn is
		// before the cancel — it already reached the pipeline either way, so
		// nothing changes by letting it through. The final is what arrives
		// after the cancel and is what would drive an unwanted response, so
		// only it needs suppressing. Blanket-suppressing everything (as
		// round 1 did) silently ate the FOLLOWING turn's own interims too —
		// cancelledGen stays outstanding until that turn's own Finalize
		// clears it — which broke barge-in (audio_pipeline.go:1790,
		// 1841-1870) and the finalize-timeout safety net
		// (audio_pipeline.go:1679-1687, 1837).
		s.turnMu.Lock()
		suppressed := evt.IsFinal && s.cancelledGen != 0
		if evt.IsFinal && !suppressed {
			s.gotFinal = true
		}
		s.turnMu.Unlock()
		if suppressed {
			continue
		}

		select {
		case s.resultChan <- evt:
		case <-s.closed:
			return
		}
	}
}

// parseMessage maps one Live API server frame to a TranscriptEvent. Finals
// arrive as serverContent.inputTranscription, interims as
// serverContent.interimInputTranscription.
//
// Everything it does not recognise is logged rather than dropped in silence.
// After setupComplete this is the ONLY place a model rejection or a goAway
// could surface, and a silently discarded one of those looks exactly like a
// toy that simply stopped answering.
func (s *geminiStreamAdapter) parseMessage(data []byte) (TranscriptEvent, bool) {
	var msg struct {
		ServerContent struct {
			InputTranscription struct {
				Text string `json:"text"`
			} `json:"inputTranscription"`
			InterimInputTranscription struct {
				Text string `json:"text"`
			} `json:"interimInputTranscription"`
		} `json:"serverContent"`
		Error  json.RawMessage `json:"error"`
		GoAway json.RawMessage `json:"goAway"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		logger.WarnCF("livekit", "Gemini STT frame not parseable as JSON", map[string]any{
			"provider": "gemini",
			"error":    err.Error(),
			"raw":      truncateForLog(string(data), 400),
		})
		return TranscriptEvent{}, false
	}

	if text := strings.TrimSpace(msg.ServerContent.InputTranscription.Text); text != "" {
		return TranscriptEvent{Text: text, IsFinal: true, Language: s.language}, true
	}
	if text := strings.TrimSpace(msg.ServerContent.InterimInputTranscription.Text); text != "" {
		return TranscriptEvent{Text: text, IsFinal: false, Language: s.language}, true
	}

	if len(msg.Error) > 0 || len(msg.GoAway) > 0 {
		logger.WarnCF("livekit", "Gemini STT server error or goAway frame", map[string]any{
			"provider": "gemini",
			"raw":      truncateForLog(string(data), 400),
		})
		return TranscriptEvent{}, false
	}

	// generationComplete and other bookkeeping frames land here; Debug, not
	// Warn, because they are expected traffic.
	logger.DebugCF("livekit", "Gemini STT frame carried no transcript", map[string]any{
		"provider": "gemini",
		"raw":      truncateForLog(string(data), 400),
	})
	return TranscriptEvent{}, false
}

// geminiSTTURL allows tests to point the dialer at a local socket.
func geminiSTTURL() string {
	if override := strings.TrimSpace(os.Getenv("GEMINI_STT_WS_URL")); override != "" {
		return override
	}
	return geminiSTTWebsocketURL
}

// normalizeGeminiLanguages turns our language setting into BCP-47 codes. An
// empty slice is the documented auto-detect value, so "auto" and "" both map
// to no pinned language.
func normalizeGeminiLanguages(lang string) []string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || l == "auto" || l == "multi" {
		return []string{}
	}
	if strings.Contains(l, "-") {
		parts := strings.SplitN(l, "-", 2)
		return []string{parts[0] + "-" + strings.ToUpper(parts[1])}
	}
	defaults := map[string]string{
		"en": "en-US", "hi": "hi-IN", "bn": "bn-IN", "ta": "ta-IN",
		"te": "te-IN", "mr": "mr-IN", "gu": "gu-IN", "kn": "kn-IN",
		"ml": "ml-IN", "pa": "pa-IN",
	}
	if code, ok := defaults[l]; ok {
		return []string{code}
	}
	return []string{l}
}
