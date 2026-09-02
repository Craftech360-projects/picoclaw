package livekit

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/media-sdk"
	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/voice/stt"
	"github.com/sipeed/picoclaw/pkg/voice/tts"
	"github.com/sipeed/picoclaw/pkg/voice/vad"
)

var voiceProviderChannelMarkerRE = regexp.MustCompile(`<\|channel\>[^<]*<channel\|>`)
var voiceReasoningBlockRE = regexp.MustCompile(`(?is)<think>.*?</think>|<thought>.*?</thought>|<reasoning>.*?</reasoning>|<analysis>.*?</analysis>`)

// Third reasoning shape, alongside the XML blocks above and the "Thinking:" lines
// below: a leading JSON object. Gemma emits `{"thought": "..."} [excited] Ooh!` —
// tool-schema bleed, since tool definitions sit in context every turn. It matched
// neither of the other two filters, so the whole blob was spoken to the child.
// ponytail: non-greedy to the first "}", so a thought body containing a literal
// brace leaves a tail. Anchored at start-of-text, which for a streamed chunk means
// the chunk start. Upgrade path is the same one the MEMO strip names: handle it in
// sentenceSplitter.Feed before sentence splitting, which fixes all three at once.
var voiceReasoningJSONRE = regexp.MustCompile(`(?is)^\s*\{\s*"(?:thought|thinking|reasoning|analysis)"\s*:.*?\}\s*`)

// Cheeko Face expression tag: "[" + 2-12 lowercase letters + "]". Only a LEADING
// tag drives the face (mirrors the firmware parser), but tags are stripped
// ANYWHERE before TTS: the model re-tags mid-utterance ("...hello [sleepy] the
// moon...") and an unanchored tag used to be read aloud as the word "sleepy".
// The tagged text still reaches the device via speech_created for the face.
var voiceExpressionTagRE = regexp.MustCompile(`\s*(?:\[[a-z]{2,12}\]\s*)+`)
var voiceLeadingTagCaptureRE = regexp.MustCompile(`^\s*\[([a-z]{2,12})\]`)

// The persona prompt ends a session with a "MEMO: ..." line summarising it and
// claims the gateway strips it; there is no gateway on the LiveKit path, so the
// line was read aloud to the child. The durable summary is unaffected: teardown
// generates its own via FinalizeSessionSummary (post_session_persistence.go).
// The primary defense is now the memoSeen latch inside sentenceSplitter.Feed,
// which cuts the stream at the memo anchor so multi-sentence memo bodies never
// reach the tokenizer. This per-chunk strip stays as defense-in-depth for text
// that bypasses the splitter (fallback greetings, filler phrases).
var voiceMemoLineRE = regexp.MustCompile(`(?im)^\s*memo\s*:.*$`)

// leadingExpressionTag returns the first leading [tag] name, or "".
func leadingExpressionTag(text string) string {
	if m := voiceLeadingTagCaptureRE.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

var voiceReasoningLineRE = regexp.MustCompile(`(?im)^\s*(thinking|reasoning|analysis)\s*[:：].*$`)

// Persona prompts use ALL-CAPS for shouting ("BLAST OFF!", "AMAZING"), but TTS
// providers (Sarvam especially) read all-caps words as acronyms and spell them
// out letter by letter ("OFF" -> "O F F"). Lowercase them before synthesis,
// keeping real initialisms spelled out.
// ponytail: static allowlist; extend if a persona ever needs a new acronym.
var voiceAllCapsWordRE = regexp.MustCompile(`\b[A-Z]{2,}\b`)
var voiceAcronymAllowlist = map[string]struct{}{
	"TV": {}, "OK": {}, "AI": {}, "USA": {}, "ABC": {}, "DIY": {}, "ID": {},
}
var dynamicGreetingCooldownUntilUnix atomic.Int64

const (
	liveKitTTSAudioTailMs       = 250
	liveKitFinalTransportTailMs = 250
	liveKitTimeoutFallbackMax   = 8 * time.Second
	bargeInDuplicateWindow      = 1500 * time.Millisecond
)

var shortUtteranceHoldPhrases = map[string]struct{}{
	"hello": {},
	"hi":    {},
	"ok":    {},
	"okay":  {},
	"hmm":   {},
	"yes":   {},
	"yeah":  {},
	"yep":   {},
}

func dynamicGreetingRateLimited() bool {
	return time.Now().Unix() < dynamicGreetingCooldownUntilUnix.Load()
}

func markDynamicGreetingRateLimited(cooldown time.Duration) {
	if cooldown <= 0 {
		cooldown = 2 * time.Minute
	}
	until := time.Now().Add(cooldown).Unix()
	dynamicGreetingCooldownUntilUnix.Store(until)
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	lowerErr := strings.ToLower(err.Error())
	return strings.Contains(lowerErr, "429") || strings.Contains(lowerErr, "rate-limit") || strings.Contains(lowerErr, "rate limit")
}

func sanitizeVoiceTextForTTS(text string) string {
	// Replaced with a space, not "": the pattern eats the whitespace on both sides,
	// so dropping a mid-sentence tag would otherwise glue words ("hello...the moon").
	// The Fields/Join at the end collapses the extra space and trims the edges.
	text = voiceExpressionTagRE.ReplaceAllString(text, " ")
	// After the tag strip so "[neutral] MEMO: ..." still anchors at line start.
	text = voiceMemoLineRE.ReplaceAllString(text, "")
	// Also after the tag strip: the model sometimes emits a tag ahead of the JSON,
	// and the pattern anchors at start-of-text.
	text = voiceReasoningJSONRE.ReplaceAllString(text, "")
	text = voiceReasoningBlockRE.ReplaceAllString(text, "")
	text = voiceReasoningLineRE.ReplaceAllString(text, "")
	text = voiceProviderChannelMarkerRE.ReplaceAllString(text, "")
	text = strings.NewReplacer(
		"<|channel|>", "",
		"<|message|>", "",
		"<|start|>", "",
		"<|end|>", "",
		"<|channel>", "",
		"<channel|>", "",
		"<think>", "",
		"</think>", "",
		"<thought>", "",
		"</thought>", "",
		"<reasoning>", "",
		"</reasoning>", "",
		"<analysis>", "",
		"</analysis>", "",
	).Replace(text)
	text = voiceAllCapsWordRE.ReplaceAllStringFunc(text, func(w string) string {
		if _, ok := voiceAcronymAllowlist[w]; ok {
			return w
		}
		return strings.ToLower(w)
	})
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func normalizeUtteranceForDuplicateCheck(text string) string {
	return strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(text), " ")))
}

func shouldHoldShortUtterance(text string) bool {
	normalized := normalizeUtteranceForDuplicateCheck(text)
	normalized = strings.Trim(normalized, " \t\r\n.,!?;:")
	if normalized == "" {
		return false
	}
	if _, ok := shortUtteranceHoldPhrases[normalized]; ok {
		return true
	}
	parts := strings.Fields(normalized)
	return len(parts) == 1 && len([]rune(parts[0])) <= 4
}

func shouldSuppressBargeInTranscript(text, lastText string, lastAt, now time.Time, pendingShortText string) bool {
	normalized := strings.Trim(normalizeUtteranceForDuplicateCheck(text), " \t\r\n.,!?;:")
	if normalized == "" || !shouldHoldShortUtterance(normalized) {
		return false
	}
	if pendingShortText != "" &&
		normalized == strings.Trim(normalizeUtteranceForDuplicateCheck(pendingShortText), " \t\r\n.,!?;:") {
		return true
	}
	lastNormalized := strings.Trim(normalizeUtteranceForDuplicateCheck(lastText), " \t\r\n.,!?;:")
	return lastNormalized == normalized && !lastAt.IsZero() && now.Sub(lastAt) < bargeInDuplicateWindow
}

func mergeFinalTranscriptChunk(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}

	normalizedCurrent := normalizeUtteranceForDuplicateCheck(current)
	normalizedNext := normalizeUtteranceForDuplicateCheck(next)
	switch {
	case normalizedNext == normalizedCurrent:
		return current
	case strings.HasPrefix(normalizedNext, normalizedCurrent):
		return next
	case strings.HasPrefix(normalizedCurrent, normalizedNext):
		return current
	default:
		return current + " " + next
	}
}

type speechChunkDeduper struct {
	last      string
	announced bool
}

func (d *speechChunkDeduper) ShouldSpeak(text string) bool {
	normalized := normalizeUtteranceForDuplicateCheck(sanitizeVoiceTextForTTS(text))
	if normalized == "" {
		return false
	}
	if len(normalized) >= 12 && normalized == d.last {
		return false
	}
	d.last = normalized
	return true
}

// memoAnchorRE matches "MEMO:" (optionally preceded by expression tags) at the
// start of the current line OR at a sentence start within it — gemma sometimes
// appends the memo after the last spoken sentence without a newline. Checked
// against the current line slice of the splitter buffer the moment a ':' is
// fed, so the anchor still exists (Feed converts newlines to spaces before
// buffering). Mid-sentence "memo:" ("I wrote a memo: ...") never matches.
var memoAnchorRE = regexp.MustCompile(`(?i)(?:^|[.!?]\s+)((?:\[[a-z]{2,12}\]\s*)*memo\s*:)$`)

// partialTagRE matches a still-incomplete expression tag ("[", "[ha") so the
// turn classifier in Feed waits instead of misreading it as speech.
var partialTagRE = regexp.MustCompile(`^\[[a-z]{0,12}$`)

// sentenceSplitter accumulates text and emits complete sentences using a tokenizer.
type sentenceSplitter struct {
	buf       strings.Builder
	tokenizer *sentences.DefaultSentenceTokenizer
	// memoSeen latches once a line-anchored "MEMO:" is fed: everything at or
	// after it is quiz bookkeeping (see quiz_state.go), never speech. A
	// splitter lives for one turn, so the latch resets with the next turn.
	memoSeen bool
	// jsonLatch drops the whole turn when its first meaningful content is a
	// '{': gemma sometimes emits a pseudo-tool-call ({"action":"write_file",...})
	// as its entire reply, which used to be read aloud to the child. No
	// legitimate spoken turn starts with a brace. jsonChecked marks that the
	// first non-tag, non-space content has been classified.
	jsonLatch   bool
	jsonChecked bool
	// lineStart is the buf offset where the current line began (updated on
	// newline runes and whenever buf is reset), preserving the line anchor
	// that the newline-to-space conversion below would otherwise destroy.
	lineStart int
}

func newSentenceSplitter() *sentenceSplitter {
	tokenizer, err := english.NewSentenceTokenizer(nil)
	if err != nil {
		// Fallback to a simple splitter if the tokenizer fails to initialize
		return &sentenceSplitter{}
	}
	return &sentenceSplitter{
		tokenizer: tokenizer,
	}
}

func (s *sentenceSplitter) Feed(r rune) string {
	if s.memoSeen || s.jsonLatch {
		return ""
	}
	// Skip newlines entirely — they don't represent sentence boundaries for TTS
	// and would cause tiny fragments like "(Verse 1)" to be sent as separate TTS calls.
	if r == '\n' || r == '\r' {
		// Replace with a space to avoid words merging across lines
		if s.buf.Len() > 0 {
			s.buf.WriteRune(' ')
		}
		s.lineStart = s.buf.Len()
		return ""
	}

	s.buf.WriteRune(r)
	text := s.buf.String()

	// Classify the turn once its first meaningful rune arrives: a leading '{'
	// (expression tags allowed ahead of it) means the model is emitting a
	// pseudo-tool-call or reasoning JSON, never speech. Drop the entire turn.
	if !s.jsonChecked {
		lead := strings.TrimSpace(voiceExpressionTagRE.ReplaceAllString(text, " "))
		// A lone '[' + letters may still become an expression tag; wait for
		// more runes before classifying.
		if lead != "" && !partialTagRE.MatchString(lead) {
			s.jsonChecked = true
			if lead[0] == '{' {
				s.jsonLatch = true
				s.buf.Reset()
				s.lineStart = 0
				return ""
			}
		}
	}

	// The ':' just fed may complete an anchored "MEMO:". Latch and drop the memo
	// before the tokenizer can emit any of its body, and release the speech
	// buffered ahead of the anchor RIGHT NOW. Holding it until Flush cost a
	// multi-second silence: the tokenizer only emits a sentence once a following
	// one confirms the boundary, so the last question before the memo would
	// otherwise wait for the entire memo line to finish generating.
	if r == ':' && s.lineStart <= len(text) {
		if idx := memoAnchorRE.FindStringSubmatchIndex(text[s.lineStart:]); idx != nil {
			pending := strings.TrimSpace(text[:s.lineStart+idx[2]])
			s.buf.Reset()
			s.lineStart = 0
			s.memoSeen = true
			return pending
		}
	}

	// Only attempt to split if we have a potential sentence or clause boundary
	if r == '.' || r == '!' || r == '?' || r == ',' || r == ';' || r == ':' {
		if s.tokenizer != nil {
			sentences := s.tokenizer.Tokenize(text)
			if len(sentences) > 1 {
				// We have at least one complete sentence and some remainder
				completeSentence := sentences[0].Text

				// Keep the rest in the buffer
				s.buf.Reset()
				s.lineStart = 0
				for i := 1; i < len(sentences); i++ {
					s.buf.WriteString(sentences[i].Text)
					if i < len(sentences)-1 {
						s.buf.WriteString(" ")
					}
				}
				return completeSentence
			} else if r == ',' || r == ';' || r == ':' {
				// For commas and other clause boundaries, flush if the clause is long enough
				// Use a higher threshold (40 chars) to avoid tiny TTS fragments
				if len(text) > 40 {
					s.buf.Reset()
					s.lineStart = 0
					return text
				}
			}
		} else {
			// Fallback to simple splitting
			return s.simpleSplit(r)
		}
	}
	return ""
}

func (s *sentenceSplitter) simpleSplit(r rune) string {
	if r == '.' || r == '!' || r == '?' || r == ',' || r == ';' || r == ':' {
		sentence := s.buf.String()
		if (r == ',' || r == ';' || r == ':') && len(sentence) <= 15 {
			return ""
		}
		s.buf.Reset()
		s.lineStart = 0
		return sentence
	}
	return ""
}

func (s *sentenceSplitter) Flush() string {
	remaining := s.buf.String()
	s.buf.Reset()
	s.lineStart = 0
	return remaining
}

// AudioPipeline coordinates STT -> Agent -> TTS for one participant in a room.
type AudioPipeline struct {
	// Set by the room session when it subscribes the audio track. sttHolder is
	// shared with the PCM writer so a reconnect swaps the stream for both;
	// reopenSTT rebuilds one with the session's original StreamOptions.
	// rebindSTT re-points everything OUTSIDE this pipeline at the replacement:
	// ParticipantState.sttStream, which is the only route the PTT turn
	// boundary has to a provider, and the empty-tap callback.
	sttHolder            *sttStreamHolder
	reopenSTT            func() (stt.TranscriptionStream, error)
	rebindSTT            func(stt.TranscriptionStream)
	session              *RoomSession
	bridge               *AgentBridge
	tts                  tts.Provider
	vadEvent             <-chan interface{}
	primaryLanguage      string // used for language-aware error fallback phrases
	characterName        string // active character name for name-aware greeting fallback
	greetingMode         string
	asyncAnnounceMode    string
	rateLimitCooldown    time.Duration
	failureCooldown      time.Duration
	turnTimeout          time.Duration
	turns                voiceTurnController
	publishSpeechCreated func(text string)
	publishAgentState    func(oldState, newState string)
	queueMu              sync.Mutex
	queuedAnnouncements  []AsyncEvent
	queueDraining        bool
	// PTT turn generations; see notePTTPress. Written from the data-channel
	// handler goroutine, read from the STT transcription goroutine.
	pttTurnGen     atomic.Uint64
	pttAnnounceGen atomic.Uint64
}

type voiceTurnController struct {
	mu     sync.Mutex
	nextID uint64
	active voiceTurn
}

type voiceTurn struct {
	id     uint64
	ctx    context.Context
	cancel context.CancelFunc
	reason string
}

type turnLatencyMetaKey struct{}

type turnLatencyMeta struct {
	mu sync.Mutex

	TurnID   uint64
	Session  string
	Path     string
	Trigger  string
	STTStart time.Time
	LLMStart time.Time
	LLMFirst time.Time
	LLMFinal time.Time
	// Set by the provider's dispatch probe when the request leaves the process.
	LLMDispatch time.Time
	TTSFirst    time.Time
	TTSFinal    time.Time
	Completed   bool

	STTFirstPartialMS      int64
	STTFirstFinalMS        int64
	LLMRequestBuildMS      int64
	LLMProviderTTFTMS      int64
	LLMFirstTokenMS        int64
	LLMFinalTokenMS        int64
	TTSFirstAudioMS        int64
	TTSFinalAudioMS        int64
	TTSFirstAudioFromSTTMS int64
	TTSFinalAudioFromSTTMS int64
	TurnTotalE2EMS         int64
}

func (c *voiceTurnController) Start(parent context.Context, reason string) voiceTurn {
	return c.StartWithTimeout(parent, reason, 0)
}

func (c *voiceTurnController) StartWithTimeout(parent context.Context, reason string, timeout time.Duration) voiceTurn {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel != nil {
		c.active.reason = reason
		c.active.cancel()
	}
	c.nextID++
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	c.active = voiceTurn{id: c.nextID, ctx: ctx, cancel: cancel, reason: reason}
	return c.active
}

func (c *voiceTurnController) Cancel(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active.cancel == nil {
		return
	}
	c.active.reason = reason
	c.active.cancel()
}

func (c *voiceTurnController) ActiveReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active.reason
}

func (c *voiceTurnController) ActiveSnapshot() (uint64, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active.id, c.active.reason, c.active.cancel != nil
}

func (c *voiceTurnController) IsActive(turn voiceTurn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return turn.id != 0 && c.active.id == turn.id && c.active.cancel != nil
}

func (c *voiceTurnController) Finish(turn voiceTurn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn.id != 0 && c.active.id == turn.id {
		c.active = voiceTurn{}
	}
}

func (c *voiceTurnController) CurrentID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active.id
}

func (ap *AudioPipeline) attachTurnLatencyMeta(turn voiceTurn, meta *turnLatencyMeta) voiceTurn {
	if turn.ctx == nil || meta == nil {
		return turn
	}
	turn.ctx = context.WithValue(turn.ctx, turnLatencyMetaKey{}, meta)
	// LLMStart is stamped before the prompt is assembled, so llm_first_token_ms
	// covers context building, disk reads and the slot wait as well as the call
	// itself. The first dispatch of the turn is the boundary between the two.
	turn.ctx = protocoltypes.WithDispatchProbe(turn.ctx, func(at time.Time) {
		meta.mu.Lock()
		defer meta.mu.Unlock()
		if meta.LLMDispatch.IsZero() {
			meta.LLMDispatch = at
		}
	})
	return turn
}

func latencyMetaFromContext(ctx context.Context) *turnLatencyMeta {
	if ctx == nil {
		return nil
	}
	meta, _ := ctx.Value(turnLatencyMetaKey{}).(*turnLatencyMeta)
	return meta
}

func (ap *AudioPipeline) logTurnLatency(meta *turnLatencyMeta, marker string, duration time.Duration, extra map[string]any) {
	if meta == nil {
		return
	}
	switch marker {
	case "stt_first_partial":
		if meta.STTFirstPartialMS == 0 {
			meta.STTFirstPartialMS = duration.Milliseconds()
		}
	case "stt_first_final":
		if meta.STTFirstFinalMS == 0 {
			meta.STTFirstFinalMS = duration.Milliseconds()
		}
	case "llm_first_token":
		if meta.LLMFirstTokenMS == 0 {
			meta.LLMFirstTokenMS = duration.Milliseconds()
			meta.mu.Lock()
			dispatch := meta.LLMDispatch
			meta.mu.Unlock()
			if !dispatch.IsZero() && !meta.LLMStart.IsZero() {
				meta.LLMRequestBuildMS = dispatch.Sub(meta.LLMStart).Milliseconds()
				meta.LLMProviderTTFTMS = meta.LLMFirstTokenMS - meta.LLMRequestBuildMS
			}
		}
	case "llm_final_token":
		meta.LLMFinalTokenMS = duration.Milliseconds()
	case "tts_first_audio":
		if meta.TTSFirstAudioMS == 0 {
			meta.TTSFirstAudioMS = duration.Milliseconds()
		}
	case "tts_final_audio":
		meta.TTSFinalAudioMS = duration.Milliseconds()
	case "tts_first_audio_from_stt_start":
		if meta.TTSFirstAudioFromSTTMS == 0 {
			meta.TTSFirstAudioFromSTTMS = duration.Milliseconds()
		}
	case "tts_final_audio_from_stt_start":
		meta.TTSFinalAudioFromSTTMS = duration.Milliseconds()
	case "turn_total_e2e":
		meta.TurnTotalE2EMS = duration.Milliseconds()
	}

	fields := map[string]any{
		"turn_id":      meta.TurnID,
		"session":      meta.Session,
		"path":         meta.Path,
		"trigger":      meta.Trigger,
		"marker":       marker,
		"elapsed_ms":   duration.Milliseconds(),
		"timestamp_ms": time.Now().UnixMilli(),
	}
	if marker == "tts_first_audio" && meta.Path == "greeting" {
		fields["first_greeting_ready_ms"] = duration.Milliseconds()
	}
	for k, v := range extra {
		fields[k] = v
	}
	logger.InfoCF("livekit", "Turn latency marker", fields)
	ap.emitRuntimeEvent("latency_marker", meta.Session, marker, "", fields)
}

func (ap *AudioPipeline) finalizeTurnLatency(meta *turnLatencyMeta, reason string) {
	if meta == nil {
		return
	}
	meta.mu.Lock()
	if meta.Completed {
		meta.mu.Unlock()
		return
	}
	meta.Completed = true
	base := meta.LLMStart
	if !meta.STTStart.IsZero() {
		base = meta.STTStart
	}
	meta.mu.Unlock()

	if !base.IsZero() {
		ap.logTurnLatency(meta, "turn_total_e2e", time.Since(base), map[string]any{"finalize_reason": reason})
	}

	logger.InfoCF("livekit", "Turn latency summary", map[string]any{
		"turn_id":                        meta.TurnID,
		"session":                        meta.Session,
		"path":                           meta.Path,
		"trigger":                        meta.Trigger,
		"finalize_reason":                reason,
		"stt_first_partial_ms":           meta.STTFirstPartialMS,
		"stt_first_final_ms":             meta.STTFirstFinalMS,
		"llm_request_build_ms":           meta.LLMRequestBuildMS,
		"llm_provider_ttft_ms":           meta.LLMProviderTTFTMS,
		"llm_first_token_ms":             meta.LLMFirstTokenMS,
		"llm_final_token_ms":             meta.LLMFinalTokenMS,
		"tts_first_audio_ms":             meta.TTSFirstAudioMS,
		"tts_final_audio_ms":             meta.TTSFinalAudioMS,
		"tts_first_audio_from_stt_ms":    meta.TTSFirstAudioFromSTTMS,
		"tts_final_audio_from_stt_ms":    meta.TTSFinalAudioFromSTTMS,
		"turn_total_e2e_ms":              meta.TurnTotalE2EMS,
		"missing_stt_final_marker":       meta.STTFirstFinalMS == 0,
		"missing_llm_first_token_marker": meta.LLMFirstTokenMS == 0,
		"missing_tts_first_audio_marker": meta.TTSFirstAudioMS == 0,
	})
}

func (ap *AudioPipeline) emitRuntimeEvent(kind, sessionKey, cause, errText string, meta map[string]any) {
	if ap == nil || ap.bridge == nil {
		return
	}
	evt := RuntimeEvent{
		Kind:       kind,
		SessionKey: sessionKey,
		Cause:      cause,
		Error:      errText,
		Metadata:   meta,
	}
	_ = ap.bridge.EmitRuntimeEvent(evt)
}

func (ap *AudioPipeline) enqueueAnnouncement(evt AsyncEvent) {
	ap.queueMu.Lock()
	ap.queuedAnnouncements = append(ap.queuedAnnouncements, evt)
	qLen := len(ap.queuedAnnouncements)
	ap.queueMu.Unlock()
	ap.emitRuntimeEvent("background_event_queued", evt.SessionKey, "runtime_policy", "", map[string]any{
		"tool":       evt.ToolName,
		"queue_size": qLen,
	})
}

func (ap *AudioPipeline) dequeueAnnouncement() (AsyncEvent, bool) {
	ap.queueMu.Lock()
	defer ap.queueMu.Unlock()
	if len(ap.queuedAnnouncements) == 0 {
		return AsyncEvent{}, false
	}
	evt := ap.queuedAnnouncements[0]
	ap.queuedAnnouncements = ap.queuedAnnouncements[1:]
	return evt, true
}

func (ap *AudioPipeline) shouldQueueAnnouncementsNow() bool {
	if ap == nil || ap.session == nil || ap.session.participant == nil {
		return false
	}
	if ap.session.participant.speaking.Load() {
		return true
	}
	if ap.turns.CurrentID() != 0 {
		return true
	}
	return false
}

func (ap *AudioPipeline) maybeDrainQueuedAnnouncements() {
	if ap == nil || !strings.EqualFold(ap.asyncAnnounceMode, "queue") {
		return
	}
	if ap.shouldQueueAnnouncementsNow() {
		return
	}

	ap.queueMu.Lock()
	if ap.queueDraining || len(ap.queuedAnnouncements) == 0 {
		ap.queueMu.Unlock()
		return
	}
	ap.queueDraining = true
	ap.queueMu.Unlock()

	go func() {
		defer func() {
			ap.queueMu.Lock()
			ap.queueDraining = false
			ap.queueMu.Unlock()
		}()

		for {
			if ap.shouldQueueAnnouncementsNow() {
				return
			}
			evt, ok := ap.dequeueAnnouncement()
			if !ok {
				return
			}
			ap.emitRuntimeEvent("background_event_dequeued", evt.SessionKey, "runtime_policy", "", map[string]any{
				"tool": evt.ToolName,
			})
			ap.handleAsyncEvent(evt, false)
		}
	}()
}

// reopenSTTStream replaces a dead STT stream, bounded so a provider that is
// rejecting us (bad key, quota) fails the session instead of spinning. Attempts
// are per-close, not per-session: a call that survives several blips is working.
func (ap *AudioPipeline) reopenSTTStream(ctx context.Context, sessionKey string) (stt.TranscriptionStream, error) {
	if ap == nil || ap.reopenSTT == nil {
		return nil, fmt.Errorf("no STT reopen available for this session")
	}
	const attempts = 3
	backoff := 250 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stream, err := ap.reopenSTT()
		if err == nil {
			// The caller that closed the old stream already released it; swapping
			// only rebinds the writer goroutine to the live one.
			ap.sttHolder.Swap(stream)
			// The writer is not the only thing bound to the old stream. The
			// PTT turn boundary reaches the provider through
			// ParticipantState.sttStream, and the empty-tap callback is
			// registered per stream: without this, every rotation left
			// ResetBuffer/CancelTurn going to the dead adapter while Finalize
			// went to the live one, so the toy went deaf for the rest of the
			// session with nothing in the log.
			if ap.rebindSTT != nil {
				ap.rebindSTT(stream)
			}
			logger.InfoCF("livekit", "STT stream reopened after close", map[string]any{
				"session": sessionKey,
				"attempt": attempt,
			})
			return stream, nil
		}
		lastErr = err
		logger.WarnCF("livekit", "STT stream reopen failed", map[string]any{
			"session": sessionKey,
			"attempt": attempt,
			"error":   err.Error(),
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

func NewAudioPipeline(session *RoomSession, bridge *AgentBridge, tts tts.Provider, vadEvent <-chan interface{}) *AudioPipeline {
	var lang, charName string
	if session != nil {
		lang = session.primaryLanguage
		charName = session.characterName
	}
	turnTimeout := 45 * time.Second
	if session != nil && session.runtime.TurnTimeoutSeconds > 0 {
		turnTimeout = time.Duration(session.runtime.TurnTimeoutSeconds) * time.Second
	}
	ap := &AudioPipeline{
		session:           session,
		bridge:            bridge,
		tts:               tts,
		vadEvent:          vadEvent,
		primaryLanguage:   lang,
		characterName:     charName,
		greetingMode:      "dynamic",
		asyncAnnounceMode: "immediate",
		rateLimitCooldown: 2 * time.Minute,
		failureCooldown:   30 * time.Second,
		turnTimeout:       turnTimeout,
	}
	if session != nil {
		mode := strings.ToLower(strings.TrimSpace(session.runtime.GreetingMode))
		if mode != "" {
			ap.greetingMode = mode
		}
		asyncMode := strings.ToLower(strings.TrimSpace(session.runtime.AsyncAnnounceMode))
		if asyncMode != "" {
			ap.asyncAnnounceMode = asyncMode
		}
		if sec := session.runtime.RateLimitCooldownSeconds; sec > 0 {
			ap.rateLimitCooldown = time.Duration(sec) * time.Second
		}
		if sec := session.runtime.ProviderFailureCooldownSec; sec > 0 {
			ap.failureCooldown = time.Duration(sec) * time.Second
		}
	}
	ap.publishSpeechCreated = func(text string) {
		if ap.session != nil {
			_ = ap.session.PublishSpeechCreated(text)
		}
	}
	ap.publishAgentState = func(oldState, newState string) {
		if ap.session != nil {
			_ = ap.session.PublishAgentState(oldState, newState)
		}
	}
	return ap
}

func (ap *AudioPipeline) startTurn(parent context.Context, reason string) voiceTurn {
	if ap == nil {
		var turns voiceTurnController
		return turns.Start(parent, reason)
	}
	return ap.turns.StartWithTimeout(parent, reason, ap.turnTimeout)
}

func (ap *AudioPipeline) publishState(oldState, newState string) {
	if ap == nil {
		return
	}
	if ap.publishAgentState != nil {
		ap.publishAgentState(oldState, newState)
		return
	}
	if ap.session != nil {
		_ = ap.session.PublishAgentState(oldState, newState)
	}
}

func (ap *AudioPipeline) resetAgentStateToListening() {
	ap.publishState("thinking", "listening")
	ap.publishState("speaking", "listening")
}

func (ap *AudioPipeline) interruptActiveSpeech(reason string) {
	if ap == nil {
		return
	}
	turnID, turnReason, hadActiveTurn := ap.turns.ActiveSnapshot()
	hadActiveTTS := ap.hasActiveTTS()
	logger.InfoCF("livekit", "Interrupting active agent audio", map[string]any{
		"session":            ap.sessionKey(),
		"reason":             reason,
		"had_active_tts":     hadActiveTTS,
		"had_active_turn":    hadActiveTurn,
		"active_turn_id":     turnID,
		"active_turn_reason": turnReason,
		"track_sid":          ap.localTrackSID(),
	})
	ap.cancelTTS(reason)
	ap.turns.Cancel(reason)
}

func (ap *AudioPipeline) hasActiveTTS() bool {
	if ap == nil || ap.session == nil || ap.session.participant == nil {
		return false
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.ttsCancel != nil
}

func (ap *AudioPipeline) speakTurnTimeoutFallback(sessionKey string, cause error) {
	if ap == nil || ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	parent := context.Background()
	if ap.session.ctx != nil {
		parent = ap.session.ctx
	}
	ctx, cancel := context.WithTimeout(parent, liveKitTimeoutFallbackMax)
	defer cancel()
	select {
	case <-ctx.Done():
		return
	default:
	}

	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	ap.emitRuntimeEvent("fallback_used", sessionKey, "turn_timeout", errText, nil)
	logger.WarnCF("livekit", "Voice turn timed out; playing fallback", map[string]any{
		"session": sessionKey,
		"error":   errText,
	})
	ap.setTTSCancel(cancel)
	ap.publishState("thinking", "speaking")
	ap.publishSpeechCreated("")
	ap.synthesizeAndPlay(ctx, ap.retryFallbackPhrase())
}

// didntHearFallbackPhrase is the empty-tap response (ADR 0007 / ptt-batch-stt/003):
// the child pressed the button but Manual Talk's batch STT returned no
// transcript for real buffered audio. English-only for v1 — the plan
// deliberately deferred translating this one phrase.
func (ap *AudioPipeline) didntHearFallbackPhrase() string {
	return "I didn't hear you! Press the button and try again."
}

// The empty-result callback carries no utterance identity (TranscriptEvent has
// no id, and issue 002 deferred adding one), so these two counters decide
// whether a late "no transcript" still deserves an announcement: a press bumps
// pttTurnGen, End Turn marks that generation announceable, and Cancel Turn
// marks none. A result whose generation is no longer current belongs to an
// utterance the child has already moved past.
//
// ponytail: two turns finalized back-to-back (press, end, press, end inside
// one REST round-trip) leave both awaiting a verdict against a single slot, so
// the first one's empty result can announce as if it were the second's. Costs
// one spurious "I didn't hear you" the child answers by pressing again. The
// real fix is per-utterance identity through the provider callback — the same
// missing correlation issue 002 deferred, worth doing once for both.
func (ap *AudioPipeline) notePTTPress() {
	if ap == nil {
		return
	}
	ap.pttTurnGen.Add(1)
	ap.pttAnnounceGen.Store(0)
}

func (ap *AudioPipeline) noteEndTurn() {
	if ap == nil {
		return
	}
	gen := ap.pttTurnGen.Load()
	if gen == 0 {
		// speech_end without a preceding press (e.g. the tap that opened the
		// session): still a legitimate End Turn.
		gen = ap.pttTurnGen.Add(1)
	}
	ap.pttAnnounceGen.Store(gen)
}

func (ap *AudioPipeline) noteCancelTurn() {
	if ap == nil {
		return
	}
	ap.pttAnnounceGen.Store(0)
}

// mayAnnounceEmptyResult reports whether an empty-result callback landing now
// belongs to the turn the child is actually waiting on.
func (ap *AudioPipeline) mayAnnounceEmptyResult() bool {
	if ap == nil {
		return false
	}
	announce := ap.pttAnnounceGen.Load()
	return announce != 0 && announce == ap.pttTurnGen.Load()
}

// speakDidntHearFallback plays the empty-tap fallback. It is wired as the
// sarvam_rest provider's empty-result callback, which the provider documents
// as firing on the transcription goroutine, not RunInbound's own — so this
// method must only touch ap.tts/ap.session/participant-locked state (exactly
// what speakTurnTimeoutFallback already touches), never RunInbound's
// loop-local variables.
func (ap *AudioPipeline) speakDidntHearFallback() {
	if ap == nil || ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	if !ap.mayAnnounceEmptyResult() {
		// Either the turn was cancelled (Cancel Turn must never announce) or
		// the child has already pressed again — this result is stale.
		return
	}
	if ap.turns.CurrentID() != 0 {
		// A real reply is already in flight — never talk over it.
		return
	}
	parent := context.Background()
	if ap.session.ctx != nil {
		parent = ap.session.ctx
	}
	ctx, cancel := context.WithTimeout(parent, liveKitTimeoutFallbackMax)
	defer cancel()
	select {
	case <-ctx.Done():
		return
	default:
	}

	sessionKey := ap.sessionKey()
	ap.emitRuntimeEvent("fallback_used", sessionKey, "ptt_empty_tap", "", nil)
	logger.InfoCF("livekit", "PTT tap produced no transcript; playing didn't-hear fallback", map[string]any{
		"session": sessionKey,
	})
	ap.setTTSCancel(cancel)
	ap.publishState("thinking", "speaking")
	ap.publishSpeechCreated("")
	ap.synthesizeAndPlay(ctx, ap.didntHearFallbackPhrase())
}

func (ap *AudioPipeline) logTextPreview(sessionKey, text string, limit int) string {
	if ap != nil && ap.bridge != nil {
		return ap.bridge.logContentPreview(sessionKey, text, limit)
	}
	if ap != nil && ap.session != nil {
		return newLogContentPolicy(
			ap.session.runtime.DetailedTraceEnabled,
			ap.session.runtime.TraceSampleRate,
		).contentPreview(sessionKey, text, limit)
	}
	return redactedLogValue
}

func (ap *AudioPipeline) logTextRedacted(sessionKey string) bool {
	if ap != nil && ap.bridge != nil {
		return !ap.bridge.logContentPolicy.enabledForSession(sessionKey)
	}
	if ap != nil && ap.session != nil {
		return !newLogContentPolicy(
			ap.session.runtime.DetailedTraceEnabled,
			ap.session.runtime.TraceSampleRate,
		).enabledForSession(sessionKey)
	}
	return true
}

// HandleUtterance processes a complete user utterance: calls the agent and speaks the response.
func (ap *AudioPipeline) HandleUtterance(ctx context.Context, sessionKey string, text string, onDone func()) (bool, error) {
	if strings.TrimSpace(text) == "" {
		if onDone != nil {
			onDone()
		}
		return false, nil
	}
	if ap.bridge == nil {
		if onDone != nil {
			onDone()
		}
		return false, fmt.Errorf("agent bridge is nil")
	}

	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}
	firstChunkReceived := false
	latencyMeta := latencyMetaFromContext(ctx)
	var fillerCtx context.Context
	var fillerCancel context.CancelFunc

	if ap.session != nil {
		ap.publishState("listening", "thinking")
	}

	// Play filler word asynchronously so we can cancel it if LLM is fast
	if ap.session != nil && len(ap.session.fillerWords) > 0 {
		fillerCtx, fillerCancel = context.WithCancel(ctx)
		filler := ap.session.fillerWords[rand.Intn(len(ap.session.fillerWords))]
		go func() {
			ap.synthesizeAndPlay(fillerCtx, filler)
		}()
	}

	// ── Retry loop: up to 3 attempts with exponential back-off ───────────────
	const maxRetries = 3
	backoff := 200 * time.Millisecond

	// Build the chunk/done callbacks once; they close over splitter & firstChunkReceived.
	onChunk := func(chunk string) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !firstChunkReceived {
			firstChunkReceived = true
			if latencyMeta != nil {
				ap.logTurnLatency(latencyMeta, "llm_first_token", time.Since(latencyMeta.LLMStart), nil)
			}
			if ap.session != nil {
				ap.publishState("thinking", "speaking")
			}
			if fillerCancel != nil {
				fillerCancel()                       // Cancel filler if LLM starts responding quickly
				ap.cancelTTS("llm_response_started") // clear any buffered filler audio
			}
		}
		for _, r := range chunk {
			if sentence := splitter.Feed(r); sentence != "" {
				ap.synthesizeDeduped(ctx, speechDeduper, sentence)
			}
		}
	}
	onDoneCallback := func() {
		select {
		case <-ctx.Done():
			if fillerCancel != nil {
				fillerCancel()
			}
			if onDone != nil {
				onDone()
			}
			return
		default:
		}
		// onDone is called when the LLM turn is finished (including async tools)
		if fillerCancel != nil {
			fillerCancel()
		}
		if remainder := splitter.Flush(); remainder != "" {
			ap.synthesizeDeduped(ctx, speechDeduper, remainder)
		}
		if latencyMeta != nil {
			ap.logTurnLatency(latencyMeta, "llm_final_token", time.Since(latencyMeta.LLMStart), nil)
		}
		ap.flushSilence(liveKitFinalTransportTailMs) // keep a short transport tail for ESP/LiveKit buffers
		if ap.session != nil {
			ap.publishState("speaking", "listening")
		}
		ap.maybeDrainQueuedAnnouncements()
		if onDone != nil {
			onDone()
		}
	}

	var asyncPending bool
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Reset splitter on each retry so partial text doesn't bleed through
		if attempt > 0 {
			splitter = newSentenceSplitter()
			firstChunkReceived = false
		}
		asyncPending, err = ap.bridge.ChatStream(ctx, sessionKey, text, onChunk, onDoneCallback)
		if err == nil {
			break
		}
		if isContextCanceled(ctx, err) {
			interruptedErr := ctx.Err()
			if interruptedErr == nil {
				interruptedErr = err
			}
			ap.emitRuntimeEvent("interruption", sessionKey, "chatstream_context_canceled", interruptedErr.Error(), nil)
			logger.InfoCF("livekit", "ChatStream interrupted by canceled turn context", map[string]any{
				"session":       sessionKey,
				"cancel_reason": ap.currentTTSCancelReason(),
				"error":         err.Error(),
			})
			return false, interruptedErr
		}
		logger.WarnCF("livekit", "ChatStream failed, retrying", map[string]any{
			"attempt": attempt + 1,
			"max":     maxRetries,
			"error":   err.Error(),
			"session": sessionKey,
		})
		ap.emitRuntimeEvent("retry_scheduled", sessionKey, "chatstream_error", err.Error(), map[string]any{
			"attempt": attempt + 1,
			"max":     maxRetries,
		})
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				// Context cancelled (e.g. user interrupted) — don't retry
				if onDone != nil {
					onDone()
				}
				return false, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}
	}

	if err != nil {
		// All retries exhausted — play a language-aware friendly fallback and clean up.
		fallback := ap.retryFallbackPhrase()
		ap.emitRuntimeEvent("fallback_used", sessionKey, "chatstream_retries_exhausted", err.Error(), nil)
		logger.ErrorCF("livekit", "All ChatStream retries failed — playing fallback", map[string]any{
			"session": sessionKey,
			"error":   err.Error(),
		})
		if fillerCancel != nil {
			fillerCancel()
		}
		ap.synthesizeAndPlay(ctx, fallback)
		if ap.session != nil {
			ap.resetAgentStateToListening()
		}
		ap.maybeDrainQueuedAnnouncements()
		if onDone != nil {
			onDone()
		}
		return false, fmt.Errorf("agent (after %d retries): %w", maxRetries, err)
	}

	// If a tool is running asynchronously, we don't clear the context immediately.
	// The AgentBridge will call onDone when all iterations are complete.
	return asyncPending, nil
}

func (ap *AudioPipeline) HandleUtteranceForTurn(turn voiceTurn, sessionKey string, text string) (bool, error) {
	asyncPending, err := ap.HandleUtterance(turn.ctx, sessionKey, text, func() {
		if !ap.turns.IsActive(turn) {
			return
		}
		if errors.Is(turn.ctx.Err(), context.DeadlineExceeded) {
			ap.speakTurnTimeoutFallback(sessionKey, turn.ctx.Err())
		}
		ap.resetAgentStateToListening()
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_done_callback")
		turn.cancel()
		ap.turns.Finish(turn)
	})
	if err != nil && isContextCanceled(turn.ctx, err) {
		if ap.turns.IsActive(turn) {
			if errors.Is(turn.ctx.Err(), context.DeadlineExceeded) {
				ap.speakTurnTimeoutFallback(sessionKey, err)
			}
			ap.resetAgentStateToListening()
		}
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_canceled")
	}
	if !asyncPending {
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "turn_sync_complete")
		ap.turns.Finish(turn)
	}
	return asyncPending, err
}

// TriggerGreeting executes a proactive dynamic LLM greeting using the bridge.
// It bypasses the user speech wait loop and talks directly into the TTS pipeline.
// retryFallbackPhrase returns a child-friendly "I glitched, say again" phrase
// in the session's primary language so the child is not left in silence.
func (ap *AudioPipeline) retryFallbackPhrase() string {
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return "Oho! Mujhe thoda confusion ho gaya. Kya tum dobara bol sakte ho?"
	case "kannada":
		return "Oho! Nanage swalpa gond aaytu. Matte heli, please?"
	case "malayalam":
		return "Oho! Ente brain oru second confused ayi. Vere helo please?"
	case "tamil":
		return "Oho! Enakku konjam confusion achu. Mela solla mudiyuma?"
	case "telugu":
		return "Oho! Naaku koddiga confusion aindi. Malli cheppagalava?"
	default:
		return "Oops! My brain got a little confused just now. Can you say that again?"
	}
}

func (ap *AudioPipeline) greetingFallbackPhrase() string {
	// Name-aware so the static fallback greeting matches the active character
	// (Bheem/Tenali/…), not a hardcoded "Cheeko". Empty -> Cheeko (back-compat).
	name := strings.TrimSpace(ap.characterName)
	if name == "" {
		name = "Cheeko"
	}
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return fmt.Sprintf("Namaste! Main %s hoon, tumhara dost. Chalo maze karte hain!", name)
	case "kannada":
		return fmt.Sprintf("Namaskara! Nanu %s, ninna snehita. Banni, fun madona!", name)
	case "malayalam":
		return fmt.Sprintf("Namaskaram! Njan %s anu, ninte suhruth. Namukku fun cheyyam!", name)
	case "tamil":
		return fmt.Sprintf("Vanakkam! Naan %s, un friend. Va, fun pannalaam!", name)
	case "telugu":
		return fmt.Sprintf("Namaskaram! Nenu %s, nee friend ni. Raa, fun cheddam!", name)
	default:
		return fmt.Sprintf("Hi! I am %s, your fun friend. Ready for an awesome chat?", name)
	}
}

func (ap *AudioPipeline) proactiveAnnouncementFallbackPhrase() string {
	switch strings.ToLower(ap.primaryLanguage) {
	case "hindi":
		return "Mujhe ek chhota update dena tha, par meri voice mein thoda hiccup aa gaya. Main jaldi phir try karti hoon."
	case "kannada":
		return "Nanage ondu chikka update helbekittu, aadre nanna voice-ge swalpa hiccup aaytu. Naanu swalpa time nantara matte try madtini."
	case "malayalam":
		return "Enikku oru cheriya update parayan undayirunnu, pakshe ente voice-il oru cheriya hiccup undayi. Njan kurachu kazhinju veendum try cheyyam."
	case "tamil":
		return "Unakku oru chinna update sollanum nu irundhen, aana en voice-ku konjam hiccup vandhuduchu. Naan konjam nerathula thirumbi try panren."
	case "telugu":
		return "Nenu oka chinna update cheppali anukunna, kani naa voice ki konchem hiccup vachindi. Konchem sepu taruvata malli try chestanu."
	default:
		return "I had a quick update for you, but my voice had a tiny hiccup. I will try again shortly."
	}
}

// TriggerGreeting executes a proactive dynamic LLM greeting using the bridge.
// It bypasses the user speech wait loop and talks directly into the TTS pipeline.
func (ap *AudioPipeline) TriggerGreetingFallbackOnly(ctx context.Context, sessionKey, reason string) {
	turn := ap.startTurn(ctx, "greeting_fallback_only")
	turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
		TurnID:   turn.id,
		Session:  sessionKey,
		Path:     "greeting",
		Trigger:  "fallback_only",
		LLMStart: time.Now(),
	})
	ap.setTTSCancel(turn.cancel)
	if ap.session != nil {
		ap.publishState("listening", "speaking")
	}
	ap.publishSpeechCreated("")
	ap.synthesizeAndPlay(turn.ctx, ap.greetingFallbackPhrase())
	if ap.turns.IsActive(turn) {
		ap.flushSilenceForContext(turn.ctx, 300)
		if ap.session != nil {
			ap.publishState("speaking", "listening")
		}
	}
	ap.maybeDrainQueuedAnnouncements()
	turn.cancel()
	ap.turns.Finish(turn)
	ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "greeting_fallback_only_complete")
	ap.emitRuntimeEvent("fallback_used", sessionKey, reason, "", nil)
}

func (ap *AudioPipeline) TriggerGreeting(ctx context.Context, sessionKey string) {
	if ap.bridge == nil || ap.session == nil {
		return
	}
	if strings.EqualFold(ap.greetingMode, "disabled") {
		return
	}
	if strings.EqualFold(ap.greetingMode, "fallback") {
		ap.TriggerGreetingFallbackOnly(ctx, sessionKey, "runtime_policy")
		return
	}
	if dynamicGreetingRateLimited() {
		turn := ap.startTurn(ctx, "greeting_rate_limited_fallback")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "greeting",
			Trigger:  "cooldown_fallback",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		ap.publishState("listening", "speaking")
		ap.publishSpeechCreated("")
		ap.synthesizeAndPlay(turn.ctx, ap.greetingFallbackPhrase())
		if ap.turns.IsActive(turn) {
			ap.flushSilenceForContext(turn.ctx, 300)
			ap.publishState("speaking", "listening")
		}
		ap.maybeDrainQueuedAnnouncements()
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "cooldown_fallback_complete")
		ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_cooldown_active", "", nil)
		logger.InfoCF("livekit", "Skipped dynamic greeting due to active rate-limit cooldown", map[string]any{
			"session": sessionKey,
		})
		return
	}

	logger.InfoCF("livekit", "Triggering dynamic agent greeting", map[string]any{
		"session": sessionKey,
	})

	ap.publishState("listening", "thinking")
	turn := ap.startTurn(ctx, "greeting")
	turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
		TurnID:   turn.id,
		Session:  sessionKey,
		Path:     "greeting",
		Trigger:  "dynamic",
		LLMStart: time.Now(),
	})
	ap.setTTSCancel(turn.cancel)
	firstChunkReceived := false
	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}

	go func() {
		err := ap.bridge.GenerateGreeting(turn.ctx, sessionKey, func(chunk string) {
			if !ap.turns.IsActive(turn) {
				return
			}
			if !firstChunkReceived {
				firstChunkReceived = true
				if meta := latencyMetaFromContext(turn.ctx); meta != nil {
					ap.logTurnLatency(meta, "llm_first_token", time.Since(meta.LLMStart), nil)
				}
				ap.publishState("thinking", "speaking")
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeDeduped(turn.ctx, speechDeduper, sentence)
				}
			}
		}, func() {
			if !ap.turns.IsActive(turn) {
				return
			}
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeDeduped(turn.ctx, speechDeduper, remainder)
			}
			if meta := latencyMetaFromContext(turn.ctx); meta != nil {
				ap.logTurnLatency(meta, "llm_final_token", time.Since(meta.LLMStart), nil)
			}
			ap.flushSilenceForContext(turn.ctx, liveKitFinalTransportTailMs)
			ap.publishState("speaking", "listening")
			ap.maybeDrainQueuedAnnouncements()
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "greeting_complete")
		})

		if err != nil {
			shouldFallback := !firstChunkReceived && turn.ctx.Err() == nil && ap.turns.IsActive(turn)
			// When the greeting LLM call is rate-limited, speak a local fallback
			// greeting so the child is not greeted with silence.
			if shouldFallback {
				if ap.session != nil {
					ap.publishState("listening", "speaking")
				}
				ap.publishSpeechCreated("")
				ap.synthesizeAndPlay(turn.ctx, ap.greetingFallbackPhrase())
				ap.flushSilenceForContext(turn.ctx, 300)
				if ap.session != nil {
					ap.publishState("speaking", "listening")
				}
				ap.maybeDrainQueuedAnnouncements()
			} else if turn.ctx.Err() != nil && ap.turns.IsActive(turn) {
				ap.resetAgentStateToListening()
				ap.maybeDrainQueuedAnnouncements()
			}
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "greeting_error")
			if isRateLimitError(err) {
				markDynamicGreetingRateLimited(ap.rateLimitCooldown)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_rate_limited", err.Error(), nil)
				logger.WarnCF("livekit", "Dynamic greeting rate-limited; used fallback greeting", map[string]any{
					"session": sessionKey,
					"error":   err.Error(),
				})
			} else {
				// Non-429 provider failures can still storm proactive turns; apply a
				// shorter cooldown while preserving local fallback speech behavior.
				markDynamicGreetingRateLimited(ap.failureCooldown)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "greeting_provider_unavailable", err.Error(), nil)
				logger.ErrorCF("livekit", "Failed to generate dynamic greeting", map[string]any{
					"session": sessionKey,
					"error":   err.Error(),
				})
			}
		}
	}()
}

// RunInbound reads STT transcription events and calls the agent on speech end.
// It also listens for background task completions via the bridge's async event channel.
func (ap *AudioPipeline) RunInbound(ctx context.Context, sttStream stt.TranscriptionStream) {
	if sttStream == nil {
		return
	}
	runSessionKey := ap.sessionKey()
	var utterance strings.Builder
	var vadSpeechEnded bool
	var speechActive bool
	// latestTranscript holds whatever arrived last, partial or final, and exists
	// only to confirm the child is really speaking so agent audio can be
	// interrupted. latestFinalText holds final text only and is what may be sent to
	// the LLM. Before the realtime STT protocol landed there were no partials, so
	// one variable served both jobs; with partials streaming in, dispatching on
	// latestTranscript started a turn on every fragment and cancelled it on the
	// next — three of five turns cancelled in a live session, and the child's
	// sentence split across them.
	var latestTranscript string
	var latestFinalText string
	var finalizeTimer *time.Timer
	var finalizeTimerC <-chan time.Time
	var segmentTimer *time.Timer
	var segmentTimerC <-chan time.Time
	var shortHoldTimer *time.Timer
	var shortHoldTimerC <-chan time.Time
	var lastFlushedText string
	var lastFlushedAt time.Time
	var sttSpeechStartAt time.Time
	var sttFirstPartialAt time.Time
	var sttFirstFinalAt time.Time
	var pendingBargeIn bool
	var lastBargeInText string
	var lastBargeInAt time.Time
	var hardCapFinalizePending bool
	var pendingShort *struct {
		Text         string
		Trigger      string
		STTStart     time.Time
		STTFirstPart time.Time
		STTFirstFin  time.Time
	}

	const sttSegmentHardCap = 25 * time.Second
	const shortUtteranceHoldDelay = 1 * time.Second

	// Get the async event channel from the bridge (may be nil)
	var asyncEvents <-chan AsyncEvent
	if ap.bridge != nil {
		asyncEvents = ap.bridge.AsyncEvents()
	}

	stopFinalizeTimer := func() {
		if finalizeTimer == nil {
			finalizeTimerC = nil
			return
		}
		if !finalizeTimer.Stop() {
			select {
			case <-finalizeTimer.C:
			default:
			}
		}
		finalizeTimer = nil
		finalizeTimerC = nil
	}

	stopSegmentTimer := func() {
		if segmentTimer == nil {
			segmentTimerC = nil
			return
		}
		if !segmentTimer.Stop() {
			select {
			case <-segmentTimer.C:
			default:
			}
		}
		segmentTimer = nil
		segmentTimerC = nil
	}

	stopShortHoldTimer := func() {
		if shortHoldTimer == nil {
			shortHoldTimerC = nil
			return
		}
		if !shortHoldTimer.Stop() {
			select {
			case <-shortHoldTimer.C:
			default:
			}
		}
		shortHoldTimer = nil
		shortHoldTimerC = nil
	}

	startShortHoldTimer := func() {
		if shortHoldTimer == nil {
			shortHoldTimer = time.NewTimer(shortUtteranceHoldDelay)
		} else {
			if !shortHoldTimer.Stop() {
				select {
				case <-shortHoldTimer.C:
				default:
				}
			}
			shortHoldTimer.Reset(shortUtteranceHoldDelay)
		}
		shortHoldTimerC = shortHoldTimer.C
	}

	startSegmentTimer := func() {
		if sttSegmentHardCap <= 0 {
			return
		}
		if segmentTimer == nil {
			segmentTimer = time.NewTimer(sttSegmentHardCap)
		} else {
			if !segmentTimer.Stop() {
				select {
				case <-segmentTimer.C:
				default:
				}
			}
			segmentTimer.Reset(sttSegmentHardCap)
		}
		segmentTimerC = segmentTimer.C
	}

	startFinalizeTimer := func() {
		const finalizeGracePeriod = 750 * time.Millisecond
		if finalizeTimer == nil {
			finalizeTimer = time.NewTimer(finalizeGracePeriod)
		} else {
			if !finalizeTimer.Stop() {
				select {
				case <-finalizeTimer.C:
				default:
				}
			}
			finalizeTimer.Reset(finalizeGracePeriod)
		}
		finalizeTimerC = finalizeTimer.C
	}

	dispatchUtterance := func(trigger, text string, speechStartSnapshot, firstPartialSnapshot, firstFinalSnapshot time.Time) {
		sessionKey := ap.sessionKey()
		normalizedText := normalizeUtteranceForDuplicateCheck(text)
		if normalizedText != "" &&
			normalizedText == lastFlushedText &&
			time.Since(lastFlushedAt) < 2*time.Second {
			logger.DebugCF("livekit", "Suppressing duplicate speech end with same text", map[string]any{
				"session":       sessionKey,
				"text":          ap.logTextPreview(sessionKey, text, 240),
				"text_len":      len(text),
				"text_redacted": ap.logTextRedacted(sessionKey),
				"trigger":       trigger,
			})
			return
		}
		lastFlushedText = normalizedText
		lastFlushedAt = time.Now()

		logger.DebugCF("livekit", "Speech end with text", map[string]any{
			"session":       sessionKey,
			"text":          ap.logTextPreview(sessionKey, text, 240),
			"text_len":      len(text),
			"text_redacted": ap.logTextRedacted(sessionKey),
			"trigger":       trigger,
		})

		if sessionKey == "" {
			return
		}

		turn := ap.startTurn(ctx, "new_user_utterance")
		latencyMeta := &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "user_turn",
			Trigger:  trigger,
			STTStart: speechStartSnapshot,
			LLMStart: time.Now(),
		}
		turn = ap.attachTurnLatencyMeta(turn, latencyMeta)
		if !speechStartSnapshot.IsZero() && !firstPartialSnapshot.IsZero() {
			ap.logTurnLatency(latencyMeta, "stt_first_partial", firstPartialSnapshot.Sub(speechStartSnapshot), nil)
		}
		if !speechStartSnapshot.IsZero() && !firstFinalSnapshot.IsZero() {
			ap.logTurnLatency(latencyMeta, "stt_first_final", firstFinalSnapshot.Sub(speechStartSnapshot), nil)
		}
		ap.setTTSCancel(turn.cancel)

		go func() {
			_, _ = ap.HandleUtteranceForTurn(turn, sessionKey, text)
		}()
	}

	// allowPartialText is the safety net, not the norm: only the finalize timeout
	// passes true, so a final that never arrives costs a delay rather than the
	// child being ignored entirely.
	flushBufferedUtterance := func(trigger string, allowPartialText bool) {
		text := strings.TrimSpace(utterance.String())
		if text == "" {
			text = strings.TrimSpace(latestFinalText)
		}
		if text == "" && allowPartialText {
			text = strings.TrimSpace(latestTranscript)
			if text != "" {
				logger.WarnCF("livekit", "Dispatching a partial transcript; no final arrived in time", map[string]any{
					"session":  ap.sessionKey(),
					"trigger":  trigger,
					"text_len": len(text),
				})
			}
		}
		utterance.Reset()
		latestTranscript = ""
		latestFinalText = ""
		vadSpeechEnded = false
		speechActive = false
		hardCapFinalizePending = false
		pendingBargeIn = false
		stopFinalizeTimer()
		stopSegmentTimer()

		if ap.session != nil && ap.session.participant != nil {
			ap.session.participant.speaking.Store(false)
		}

		if text == "" {
			return
		}
		speechStartSnapshot := sttSpeechStartAt
		firstPartialSnapshot := sttFirstPartialAt
		firstFinalSnapshot := sttFirstFinalAt
		sttSpeechStartAt = time.Time{}
		sttFirstPartialAt = time.Time{}
		sttFirstFinalAt = time.Time{}

		if pendingShort != nil {
			stopShortHoldTimer()
			text = mergeFinalTranscriptChunk(pendingShort.Text, text)
			trigger = pendingShort.Trigger + "+merged"
			if speechStartSnapshot.IsZero() || (!pendingShort.STTStart.IsZero() && pendingShort.STTStart.Before(speechStartSnapshot)) {
				speechStartSnapshot = pendingShort.STTStart
			}
			if firstPartialSnapshot.IsZero() || (!pendingShort.STTFirstPart.IsZero() && pendingShort.STTFirstPart.Before(firstPartialSnapshot)) {
				firstPartialSnapshot = pendingShort.STTFirstPart
			}
			if firstFinalSnapshot.IsZero() || (!pendingShort.STTFirstFin.IsZero() && pendingShort.STTFirstFin.Before(firstFinalSnapshot)) {
				firstFinalSnapshot = pendingShort.STTFirstFin
			}
			pendingShort = nil
		}

		if shouldHoldShortUtterance(text) {
			pendingShort = &struct {
				Text         string
				Trigger      string
				STTStart     time.Time
				STTFirstPart time.Time
				STTFirstFin  time.Time
			}{
				Text:         text,
				Trigger:      trigger,
				STTStart:     speechStartSnapshot,
				STTFirstPart: firstPartialSnapshot,
				STTFirstFin:  firstFinalSnapshot,
			}
			startShortHoldTimer()
			sessionKey := ap.sessionKey()
			logger.InfoCF("livekit", "Holding short utterance before LLM dispatch", map[string]any{
				"session":       ap.sessionKey(),
				"text":          ap.logTextPreview(sessionKey, text, 240),
				"text_len":      len(text),
				"text_redacted": ap.logTextRedacted(sessionKey),
				"hold_delay_ms": shortUtteranceHoldDelay.Milliseconds(),
			})
			return
		}

		dispatchUtterance(trigger, text, speechStartSnapshot, firstPartialSnapshot, firstFinalSnapshot)
	}

	for {
		select {
		case <-ctx.Done():
			stopFinalizeTimer()
			stopSegmentTimer()
			stopShortHoldTimer()
			return

		case vadEvt, ok := <-ap.vadEvent:
			if !ok {
				ap.vadEvent = nil
				continue
			}
			if evt, ok := vadEvt.(vad.VADEvent); ok {
				if evt.SpeechStart {
					sttSpeechStartAt = time.Now()
					sttFirstPartialAt = time.Time{}
					sttFirstFinalAt = time.Time{}
					if vadSpeechEnded && (utterance.Len() > 0 || strings.TrimSpace(latestTranscript) != "") {
						flushBufferedUtterance("next_vad_start", false)
					}
					if ap.session != nil && ap.session.participant != nil {
						ap.session.participant.speaking.Store(true)
					}
					logger.DebugCF("livekit", "VAD Speech start", map[string]any{
						"session":     runSessionKey,
						"probability": evt.Probability,
					})
					logger.DebugCF("livekit", "VAD speech start detected; waiting for transcript before interrupting agent audio", map[string]any{
						"session":     runSessionKey,
						"probability": evt.Probability,
					})
					pendingBargeIn = true
					stopFinalizeTimer()
					vadSpeechEnded = false
					speechActive = true
					hardCapFinalizePending = false
					startSegmentTimer()
					lastFlushedText = ""
				}

				if evt.SpeechEnd {
					vadSpeechEnded = true
					speechActive = false
					hardCapFinalizePending = false
					stopSegmentTimer()
					logger.DebugCF("livekit", "VAD Speech end, finalizing STT stream", map[string]any{
						"session":     runSessionKey,
						"probability": evt.Probability,
					})
					if err := sttStream.Finalize(); err != nil {
						logger.ErrorCF("livekit", "Failed to finalize STT stream", map[string]any{
							"session": runSessionKey,
							"error":   err.Error(),
						})
					}
					startFinalizeTimer()
				}
			}

		case evt, ok := <-sttStream.Results():
			if !ok {
				// A closed result channel used to end RunInbound outright, which left
				// the child talking to a toy that could no longer hear anything for the
				// rest of the call. Reopen instead: a dead STT stream should cost a
				// gap, not the conversation.
				replacement, rerr := ap.reopenSTTStream(ctx, runSessionKey)
				if rerr != nil {
					logger.WarnCF("livekit", "STT stream closed and could not be reopened; exiting RunInbound", map[string]any{
						"session": runSessionKey,
						"error":   rerr.Error(),
					})
					return
				}
				sttStream = replacement
				continue
			}

			if evt.Text != "" {
				latestTranscript = evt.Text
				if sttFirstPartialAt.IsZero() {
					sttFirstPartialAt = time.Now()
				}
				if pendingBargeIn {
					now := time.Now()
					pendingShortText := ""
					if pendingShort != nil {
						pendingShortText = pendingShort.Text
					}
					if shouldSuppressBargeInTranscript(evt.Text, lastBargeInText, lastBargeInAt, now, pendingShortText) {
						sessionKey := ap.sessionKey()
						logger.InfoCF("livekit", "Suppressing duplicate short barge-in transcript", map[string]any{
							"session":       ap.sessionKey(),
							"text":          ap.logTextPreview(sessionKey, evt.Text, 240),
							"text_len":      len(evt.Text),
							"window_ms":     bargeInDuplicateWindow.Milliseconds(),
							"last_text":     ap.logTextPreview(sessionKey, lastBargeInText, 240),
							"pending_text":  ap.logTextPreview(sessionKey, pendingShortText, 240),
							"text_redacted": ap.logTextRedacted(sessionKey),
						})
					} else {
						sessionKey := ap.sessionKey()
						logger.InfoCF("livekit", "Transcript confirmed user speech; interrupting active agent audio if present", map[string]any{
							"session":       sessionKey,
							"text":          ap.logTextPreview(sessionKey, evt.Text, 240),
							"text_len":      len(evt.Text),
							"text_redacted": ap.logTextRedacted(sessionKey),
						})
						ap.interruptActiveSpeech("stt_transcript_after_vad")
						lastBargeInText = evt.Text
						lastBargeInAt = now
					}
					pendingBargeIn = false
				}
			}

			if evt.IsFinal && evt.Text != "" {
				if sttFirstFinalAt.IsZero() {
					sttFirstFinalAt = time.Now()
				}
				merged := mergeFinalTranscriptChunk(utterance.String(), evt.Text)
				utterance.Reset()
				utterance.WriteString(merged)
				latestFinalText = merged
			}

			if evt.SpeechEnd && hardCapFinalizePending && speechActive && !vadSpeechEnded {
				// Hard-cap finalize yields provider-level speech end, but user is still
				// speaking. Keep collecting transcript and rotate to the next segment.
				hardCapFinalizePending = false
				startSegmentTimer()
				continue
			}

			// Only final text reaches the LLM. A speech-end with nothing final yet —
			// Sarvam's own vad.speech_end arrives without text — leaves the state
			// alone so the finalize timer can wait for the final instead of
			// dispatching a fragment and cancelling it on the next one.
			if utterance.Len() > 0 && (evt.SpeechEnd || vadSpeechEnded) {
				trigger := "stt_speech_end"
				if vadSpeechEnded && !evt.SpeechEnd {
					trigger = "vad_with_final_text"
				}
				flushBufferedUtterance(trigger, false)
			}

		case <-finalizeTimerC:
			finalizeTimer = nil
			finalizeTimerC = nil
			if vadSpeechEnded && (utterance.Len() > 0 || strings.TrimSpace(latestTranscript) != "") {
				flushBufferedUtterance("vad_finalize_timeout", true)
			} else {
				pendingBargeIn = false
				vadSpeechEnded = false
				if ap.session != nil && ap.session.participant != nil {
					ap.session.participant.speaking.Store(false)
				}
				ap.maybeDrainQueuedAnnouncements()
			}

		case <-shortHoldTimerC:
			shortHoldTimer = nil
			shortHoldTimerC = nil
			if pendingShort == nil {
				continue
			}
			if speechActive || pendingBargeIn {
				startShortHoldTimer()
				continue
			}
			held := pendingShort
			pendingShort = nil
			dispatchUtterance(
				held.Trigger+"+hold_timeout",
				held.Text,
				held.STTStart,
				held.STTFirstPart,
				held.STTFirstFin,
			)

		case <-segmentTimerC:
			segmentTimer = nil
			segmentTimerC = nil
			if !speechActive || vadSpeechEnded {
				continue
			}
			logger.WarnCF("livekit", "STT hard-cap reached, finalizing rolling segment", map[string]any{
				"session":  ap.sessionKey(),
				"cap_secs": int(sttSegmentHardCap.Seconds()),
			})
			hardCapFinalizePending = true
			if err := sttStream.Finalize(); err != nil {
				hardCapFinalizePending = false
				logger.ErrorCF("livekit", "Failed to finalize STT stream on hard-cap", map[string]any{
					"session": ap.sessionKey(),
					"error":   err.Error(),
				})
				startSegmentTimer()
			}

		case asyncEvt, ok := <-asyncEvents:
			if !ok {
				asyncEvents = nil
				continue
			}
			ap.handleAsyncEvent(asyncEvt, vadSpeechEnded)
		}
	}
}

// handleAsyncEvent processes a background task completion event.
// If the user is currently speaking, the result is silently appended to history.
// If the user is NOT speaking, a spontaneous LLM + TTS response is triggered.
func (ap *AudioPipeline) handleAsyncEvent(evt AsyncEvent, userSpeaking bool) {
	sessionKey := ap.sessionKey()
	if sessionKey == "" {
		return
	}

	// Check if user is actively speaking
	isSpeaking := userSpeaking
	if ap.session != nil && ap.session.participant != nil {
		isSpeaking = ap.session.participant.speaking.Load()
	}

	// Runtime queue policy: defer announcements while user/agent is busy.
	// When idle, process queued events FIFO before the latest event.
	if strings.EqualFold(ap.asyncAnnounceMode, "queue") {
		if isSpeaking || ap.turns.CurrentID() != 0 {
			ap.enqueueAnnouncement(evt)
			return
		}
		if queuedEvt, ok := ap.dequeueAnnouncement(); ok {
			ap.enqueueAnnouncement(evt)
			evt = queuedEvt
		}
	}

	if isSpeaking {
		// User is speaking — silently add to conversation history.
		// The LLM will naturally see it on the next user-initiated turn.
		logger.InfoCF("livekit", "Background task result queued silently (user speaking)", map[string]any{
			"tool":    evt.ToolName,
			"session": sessionKey,
		})
		if ap.bridge != nil && ap.bridge.sessions != nil && evt.Result != nil {
			ap.bridge.sessions.AddMessage(sessionKey, "user",
				"[Background Task Completed] "+evt.ToolName+": "+evt.Result.ContentForLLM())
		}
		return
	}

	if strings.EqualFold(ap.asyncAnnounceMode, "silent_append") {
		if ap.bridge != nil && ap.bridge.sessions != nil && evt.Result != nil {
			ap.bridge.sessions.AddMessage(sessionKey, "user",
				"[Background Task Completed] "+evt.ToolName+": "+evt.Result.ContentForLLM())
		}
		ap.emitRuntimeEvent("background_event_silent_appended", sessionKey, "runtime_policy", "", map[string]any{
			"tool": evt.ToolName,
		})
		return
	}

	// User is NOT speaking — trigger spontaneous announcement
	// Cron jobs already executed through the agent path. Announce their result
	// directly to avoid an extra LLM turn.
	if evt.ToolName == "cron" && evt.Result != nil {
		content := strings.TrimSpace(evt.Result.ContentForLLM())
		if content == "" {
			return
		}
		turn := ap.startTurn(context.Background(), "background_cron_announcement")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "background_cron",
			Trigger:  "cron_direct_announcement",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		if ap.session != nil {
			ap.publishState("listening", "speaking")
			ap.publishSpeechCreated("")
		}
		ap.synthesizeAndPlay(turn.ctx, content)
		if ap.turns.IsActive(turn) && ap.session != nil {
			ap.flushSilenceForContext(turn.ctx, liveKitFinalTransportTailMs)
			ap.publishState("speaking", "listening")
		}
		ap.maybeDrainQueuedAnnouncements()
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_cron_complete")
		return
	}

	logger.InfoCF("livekit", "Triggering spontaneous announcement for background task", map[string]any{
		"tool":    evt.ToolName,
		"session": sessionKey,
	})

	if dynamicGreetingRateLimited() {
		turn := ap.startTurn(context.Background(), "background_task_cooldown_fallback")
		turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
			TurnID:   turn.id,
			Session:  sessionKey,
			Path:     "background_task",
			Trigger:  "cooldown_fallback",
			LLMStart: time.Now(),
		})
		ap.setTTSCancel(turn.cancel)
		if ap.session != nil {
			ap.publishState("listening", "speaking")
			ap.publishSpeechCreated("")
		}
		ap.synthesizeAndPlay(turn.ctx, ap.proactiveAnnouncementFallbackPhrase())
		if ap.turns.IsActive(turn) && ap.session != nil {
			ap.flushSilenceForContext(turn.ctx, 300)
			ap.publishState("speaking", "listening")
		}
		ap.maybeDrainQueuedAnnouncements()
		turn.cancel()
		ap.turns.Finish(turn)
		ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_cooldown_fallback_complete")
		ap.emitRuntimeEvent("fallback_used", sessionKey, "background_cooldown_active", "", map[string]any{
			"tool": evt.ToolName,
		})
		logger.InfoCF("livekit", "Skipped dynamic spontaneous response due to active rate-limit cooldown", map[string]any{
			"tool":    evt.ToolName,
			"session": sessionKey,
		})
		return
	}

	turn := ap.startTurn(context.Background(), "background_task_result")
	turn = ap.attachTurnLatencyMeta(turn, &turnLatencyMeta{
		TurnID:   turn.id,
		Session:  sessionKey,
		Path:     "background_task",
		Trigger:  evt.ToolName,
		LLMStart: time.Now(),
	})
	ap.setTTSCancel(turn.cancel)

	splitter := newSentenceSplitter()
	speechDeduper := &speechChunkDeduper{}

	go func() {
		if ap.session != nil {
			ap.publishState("listening", "thinking")
		}
		firstChunkReceived := false

		err := ap.bridge.GenerateSpontaneousResponse(turn.ctx, sessionKey, evt, func(chunk string) {
			if !ap.turns.IsActive(turn) {
				return
			}
			if !firstChunkReceived {
				firstChunkReceived = true
				if meta := latencyMetaFromContext(turn.ctx); meta != nil {
					ap.logTurnLatency(meta, "llm_first_token", time.Since(meta.LLMStart), nil)
				}
				if ap.session != nil {
					ap.publishState("thinking", "speaking")
				}
			}
			for _, r := range chunk {
				if sentence := splitter.Feed(r); sentence != "" {
					ap.synthesizeDeduped(turn.ctx, speechDeduper, sentence)
				}
			}
		}, func() {
			if !ap.turns.IsActive(turn) {
				return
			}
			if remainder := splitter.Flush(); remainder != "" {
				ap.synthesizeDeduped(turn.ctx, speechDeduper, remainder)
			}
			if meta := latencyMetaFromContext(turn.ctx); meta != nil {
				ap.logTurnLatency(meta, "llm_final_token", time.Since(meta.LLMStart), nil)
			}
			ap.flushSilenceForContext(turn.ctx, liveKitFinalTransportTailMs) // trailing silence for spontaneous replies
			if ap.session != nil {
				ap.publishState("speaking", "listening")
			}
			ap.maybeDrainQueuedAnnouncements()
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_task_complete")
		})
		if err != nil {
			shouldFallback := !firstChunkReceived && turn.ctx.Err() == nil && ap.turns.IsActive(turn)
			if shouldFallback {
				if ap.session != nil {
					ap.publishState("listening", "speaking")
					ap.publishSpeechCreated("")
				}
				ap.synthesizeAndPlay(turn.ctx, ap.proactiveAnnouncementFallbackPhrase())
				ap.flushSilenceForContext(turn.ctx, 300)
				if ap.session != nil {
					ap.publishState("speaking", "listening")
				}
				ap.maybeDrainQueuedAnnouncements()
			} else if turn.ctx.Err() != nil && ap.turns.IsActive(turn) {
				ap.resetAgentStateToListening()
				ap.maybeDrainQueuedAnnouncements()
			}
			if isRateLimitError(err) {
				markDynamicGreetingRateLimited(ap.rateLimitCooldown)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "background_rate_limited", err.Error(), map[string]any{
					"tool": evt.ToolName,
				})
				logger.WarnCF("livekit", "Spontaneous response rate-limited; used fallback announcement", map[string]any{
					"error":   err.Error(),
					"tool":    evt.ToolName,
					"session": sessionKey,
				})
			} else {
				markDynamicGreetingRateLimited(ap.failureCooldown)
				ap.emitRuntimeEvent("fallback_used", sessionKey, "background_provider_unavailable", err.Error(), map[string]any{
					"tool": evt.ToolName,
				})
				logger.ErrorCF("livekit", "Spontaneous response generation failed; used fallback announcement", map[string]any{
					"error":   err.Error(),
					"tool":    evt.ToolName,
					"session": sessionKey,
				})
			}
			turn.cancel()
			ap.turns.Finish(turn)
			ap.finalizeTurnLatency(latencyMetaFromContext(turn.ctx), "background_task_error")
			logger.DebugCF("livekit", "Spontaneous response turn finalized after error", map[string]any{
				"error":   err.Error(),
				"tool":    evt.ToolName,
				"session": sessionKey,
			})
		}
	}()
}

func (ap *AudioPipeline) synthesizeAndPlay(ctx context.Context, text string) {
	if ap.tts == nil || ap.session == nil || ap.session.localTrack == nil {
		return
	}
	// Capture the per-sentence emotion tag before sanitize strips it; emotion-aware
	// providers (Sarvam pace/temperature) read it from the context.
	if emotion := leadingExpressionTag(text); emotion != "" {
		ctx = tts.WithEmotion(ctx, emotion)
	}
	text = sanitizeVoiceTextForTTS(text)
	if text == "" {
		return
	}
	logger.DebugCF("livekit", "Synthesizing audio chunk", map[string]any{
		"session":           ap.sessionKey(),
		"text":              ap.logTextPreview(ap.sessionKey(), text, 240),
		"text_len":          len(text),
		"text_redacted":     ap.logTextRedacted(ap.sessionKey()),
		"tts_provider_type": fmt.Sprintf("%T", ap.tts),
	})
	stream, err := ap.tts.Synthesize(ctx, text)
	if err != nil {
		if isContextCanceled(ctx, err) {
			logger.InfoCF("livekit", "TTS synthesize interrupted by canceled turn context", map[string]any{
				"session":       ap.sessionKey(),
				"cancel_reason": ap.currentTTSCancelReason(),
				"error":         err.Error(),
			})
			return
		}
		logger.ErrorCF("livekit", "TTS synthesize failed", map[string]any{
			"session": ap.sessionKey(),
			"error":   err.Error(),
		})
		return
	}
	defer stream.Close()
	audioCapture := newTTSAudioRecorder(ap.sessionKey(), ap.session.sampleRate)
	defer audioCapture.Finalize("stream_closed")
	pcmBytes := &pcm16ByteAssembler{}
	defer func() {
		if pcmBytes.PendingLen() > 0 {
			logger.WarnCF("livekit", "Dropped trailing incomplete PCM16 byte at TTS stream end", map[string]any{
				"session":       ap.sessionKey(),
				"pending_bytes": pcmBytes.PendingLen(),
			})
		}
	}()

	logger.DebugCF("livekit", "Audio stream started", map[string]any{
		"session":   ap.sessionKey(),
		"track_sid": ap.localTrackSID(),
	})
	latencyMeta := latencyMetaFromContext(ctx)

	started := time.Now()
	wroteAudio := false
	chunksWritten := 0
	samplesWritten := 0
	for {
		select {
		case <-ctx.Done():
			logger.InfoCF("livekit", "TTS audio stream interrupted by canceled turn context", map[string]any{
				"session":         ap.sessionKey(),
				"track_sid":       ap.localTrackSID(),
				"cancel_reason":   ap.currentTTSCancelReason(),
				"chunks_written":  chunksWritten,
				"samples_written": samplesWritten,
				"duration_ms":     time.Since(started).Milliseconds(),
			})
			return
		default:
		}

		chunk, err := readAudioChunk(ctx, stream)
		if err == io.EOF {
			if latencyMeta != nil {
				latencyMeta.mu.Lock()
				if latencyMeta.TTSFinal.IsZero() {
					latencyMeta.TTSFinal = time.Now()
					llmBase := latencyMeta.LLMStart
					sttBase := latencyMeta.STTStart
					latencyMeta.mu.Unlock()
					if !llmBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_final_audio", time.Since(llmBase), nil)
					}
					if !sttBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_final_audio_from_stt_start", time.Since(sttBase), nil)
					}
				} else {
					latencyMeta.mu.Unlock()
				}
			}
			logger.DebugCF("livekit", "Audio stream complete", map[string]any{
				"session":         ap.sessionKey(),
				"track_sid":       ap.localTrackSID(),
				"chunks_written":  chunksWritten,
				"samples_written": samplesWritten,
				"duration_ms":     time.Since(started).Milliseconds(),
			})
			if wroteAudio {
				ap.writeTTSAudioTail(ctx, liveKitTTSAudioTailMs)
			}
			return
		}
		if err != nil {
			if isContextCanceled(ctx, err) {
				logger.InfoCF("livekit", "TTS stream read interrupted by canceled turn context", map[string]any{
					"session":         ap.sessionKey(),
					"track_sid":       ap.localTrackSID(),
					"cancel_reason":   ap.currentTTSCancelReason(),
					"chunks_written":  chunksWritten,
					"samples_written": samplesWritten,
					"duration_ms":     time.Since(started).Milliseconds(),
					"error":           err.Error(),
				})
				return
			}
			logger.ErrorCF("livekit", "TTS stream read failed", map[string]any{
				"session":   ap.sessionKey(),
				"track_sid": ap.localTrackSID(),
				"error":     err.Error(),
			})
			return
		}
		if len(chunk) == 0 {
			continue
		}

		alignedChunk := pcmBytes.Push(chunk)
		if len(alignedChunk) == 0 {
			continue
		}

		audioCapture.AppendPCM(alignedChunk)

		samples := bytesToPCM16(alignedChunk)
		if len(samples) == 0 {
			continue
		}
		if err := ap.session.localTrack.WriteSample(samples); err != nil {
			logger.ErrorCF("livekit", "TTS write failed", map[string]any{
				"session": ap.sessionKey(),
				"error":   err.Error(),
			})
			return
		}
		chunksWritten++
		samplesWritten += len(samples)
		if !wroteAudio {
			wroteAudio = true
			if latencyMeta != nil {
				latencyMeta.mu.Lock()
				if latencyMeta.TTSFirst.IsZero() {
					latencyMeta.TTSFirst = time.Now()
					llmBase := latencyMeta.LLMStart
					sttBase := latencyMeta.STTStart
					latencyMeta.mu.Unlock()
					if !llmBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_first_audio", time.Since(llmBase), nil)
					}
					if !sttBase.IsZero() {
						ap.logTurnLatency(latencyMeta, "tts_first_audio_from_stt_start", time.Since(sttBase), nil)
					}
				} else {
					latencyMeta.mu.Unlock()
				}
			}
			minSample, maxSample, avgAbs := sampleStats(samples)
			logger.DebugCF("livekit", "TTS audio written", map[string]any{
				"session":      ap.sessionKey(),
				"sample_count": len(samples),
				"min_sample":   minSample,
				"max_sample":   maxSample,
				"avg_abs":      avgAbs,
			})
		}
	}
}

type audioChunkResult struct {
	chunk []byte
	err   error
}

func readAudioChunk(ctx context.Context, stream tts.AudioStream) ([]byte, error) {
	if stream == nil {
		return nil, io.EOF
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan audioChunkResult, 1)
	go func() {
		chunk, err := stream.Read()
		result <- audioChunkResult{chunk: chunk, err: err}
	}()
	select {
	case res := <-result:
		return res.chunk, res.err
	case <-ctx.Done():
		_ = stream.Close()
		return nil, ctx.Err()
	}
}

func (ap *AudioPipeline) synthesizeDeduped(ctx context.Context, deduper *speechChunkDeduper, text string) {
	if deduper != nil && !deduper.ShouldSpeak(text) {
		sessionKey := ap.sessionKey()
		cleanText := sanitizeVoiceTextForTTS(text)
		logger.InfoCF("livekit", "Suppressing duplicate assistant speech chunk", map[string]any{
			"session":       sessionKey,
			"text":          ap.logTextPreview(sessionKey, cleanText, 240),
			"text_len":      len(cleanText),
			"text_redacted": ap.logTextRedacted(sessionKey),
		})
		return
	}
	// Every spoken chunk carries its raw (expression-tagged) text to the device
	// via speech_created, so the client can show the per-sentence emotion. The
	// gateway turns the first chunk of a turn into tts_start and the rest into
	// tts_sentence_start.
	if deduper != nil {
		deduper.announced = true
	}
	if ap.publishSpeechCreated != nil {
		ap.publishSpeechCreated(strings.TrimSpace(text))
	}
	ap.synthesizeAndPlay(ctx, text)
}

func (ap *AudioPipeline) cancelTTS(reason string) {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	hadActiveTTS := ps.ttsCancel != nil
	if ps.ttsCancel != nil {
		ps.ttsCancelReason = reason
		ps.ttsCancel()
		ps.ttsCancel = nil
	}
	if ap.session.localTrack != nil {
		ap.session.localTrack.ClearQueue()
	}

	if hadActiveTTS {
		ap.publishState("speaking", "listening")
		ap.emitRuntimeEvent("interruption", ap.sessionKey(), reason, "", map[string]any{
			"had_active_tts": hadActiveTTS,
		})
	}

	logger.DebugCF("livekit", "TTS cancel/clear queue", map[string]any{
		"session":        ap.sessionKey(),
		"reason":         reason,
		"had_active_tts": hadActiveTTS,
		"track_sid":      ap.localTrackSID(),
	})
}

func (ap *AudioPipeline) setTTSCancel(cancel context.CancelFunc) {
	if ap.session == nil || ap.session.participant == nil {
		return
	}
	ps := ap.session.participant
	ps.mu.Lock()
	ps.ttsCancel = cancel
	ps.ttsCancelReason = ""
	ps.mu.Unlock()
}

func (ap *AudioPipeline) currentTTSCancelReason() string {
	if ap.session == nil || ap.session.participant == nil {
		if ap.turns.ActiveReason() != "" {
			return ap.turns.ActiveReason()
		}
		return ""
	}
	ps := ap.session.participant
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.ttsCancelReason != "" {
		return ps.ttsCancelReason
	}
	return ap.turns.ActiveReason()
}

func isContextCanceled(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled)
}

func (ap *AudioPipeline) sessionKey() string {
	if ap.session == nil || ap.session.participant == nil {
		return ""
	}
	if ap.session.participant.sessionKey != "" {
		return ap.session.participant.sessionKey
	}
	if ap.session.roomInfo == nil {
		return ""
	}
	return fmt.Sprintf("livekit:%s:%s", ap.session.roomInfo.Name, ap.session.participant.identity)
}

func (ap *AudioPipeline) localTrackSID() string {
	if ap.session == nil {
		return ""
	}
	return ap.session.localTrackSID
}

func bytesToPCM16(b []byte) media.PCM16Sample {
	if len(b) < 2 {
		return nil
	}
	count := len(b) / 2
	out := make(media.PCM16Sample, count)
	for i := 0; i < count; i++ {
		off := i * 2
		out[i] = int16(binary.LittleEndian.Uint16(b[off : off+2]))
	}
	return out
}

type pcm16ByteAssembler struct {
	pending []byte
}

func (a *pcm16ByteAssembler) Push(chunk []byte) []byte {
	if a == nil || len(chunk) == 0 {
		return nil
	}

	if len(a.pending) > 0 {
		combined := make([]byte, 0, len(a.pending)+len(chunk))
		combined = append(combined, a.pending...)
		combined = append(combined, chunk...)
		chunk = combined
		a.pending = nil
	}

	if len(chunk)%2 == 0 {
		return chunk
	}

	a.pending = append(a.pending[:0], chunk[len(chunk)-1])
	return chunk[:len(chunk)-1]
}

func (a *pcm16ByteAssembler) PendingLen() int {
	if a == nil {
		return 0
	}
	return len(a.pending)
}

func sampleStats(samples media.PCM16Sample) (int16, int16, int) {
	if len(samples) == 0 {
		return 0, 0, 0
	}

	minSample := samples[0]
	maxSample := samples[0]
	var totalAbs int64
	for _, sample := range samples {
		if sample < minSample {
			minSample = sample
		}
		if sample > maxSample {
			maxSample = sample
		}
		totalAbs += int64(math.Abs(float64(sample)))
	}

	return minSample, maxSample, int(totalAbs / int64(len(samples)))
}

func ttsAudioTailSampleCount(sampleRate, durationMs int) int {
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	if durationMs <= 0 {
		return 0
	}
	return (sampleRate * durationMs) / 1000
}

func (ap *AudioPipeline) writeTTSAudioTail(ctx context.Context, durationMs int) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	if ap.session == nil || ap.session.localTrack == nil {
		return
	}
	sampleCount := ttsAudioTailSampleCount(ap.session.sampleRate, durationMs)
	if sampleCount == 0 {
		return
	}
	if err := ap.session.localTrack.WriteSample(make(media.PCM16Sample, sampleCount)); err != nil {
		logger.WarnCF("livekit", "TTS audio tail write failed", map[string]any{
			"session":     ap.sessionKey(),
			"duration_ms": durationMs,
			"error":       err.Error(),
		})
		return
	}
	logger.DebugCF("livekit", "TTS audio tail written", map[string]any{
		"session":      ap.sessionKey(),
		"duration_ms":  durationMs,
		"sample_count": sampleCount,
	})
}

// flushSilence pushes empty audio samples to ensure the end of speech is not cut off by network or device buffers.
func (ap *AudioPipeline) flushSilence(durationMs int) {
	ap.flushSilenceForContext(context.Background(), durationMs)
}

func (ap *AudioPipeline) flushSilenceForContext(ctx context.Context, durationMs int) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	if ap.session == nil || ap.session.localTrack == nil {
		return
	}
	sr := ap.session.sampleRate
	if sr == 0 {
		sr = 48000 // default fallback if 0
	}
	sampleCount := (sr * durationMs) / 1000
	samples := make(media.PCM16Sample, sampleCount)
	_ = ap.session.localTrack.WriteSample(samples)

	// Block until the entire RTP queue (including this silence) has been
	// transmitted, so the tts_stop message fires only after the device
	// has finished playing the last word.
	ap.session.localTrack.WaitForPlayout()

	logger.DebugCF("livekit", "Silence flushed – playout complete", map[string]any{
		"session":     ap.sessionKey(),
		"duration_ms": durationMs,
	})
}
